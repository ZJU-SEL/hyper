package network

import (
	"fmt"
	"net"
	"io"
	"os"
	"syscall"
	"strings"
	"sync"
	"unsafe"
	"encoding/binary"
	"sync/atomic"
	"dvm/lib/glog"
	"dvm/api/network/ipallocator"
)

const (
	DefaultNetworkBridge     = "dvm0"
)

const (
	IFNAMSIZ          = 16
	DEFAULT_CHANGE    = 0xFFFFFFFF
	SIOC_BRADDBR      = 0x89a0
	SIOC_BRDELBR      = 0x89a1
	SIOC_BRADDIF      = 0x89a2
	CIFF_TAP	  = 0x0002
	CIFF_NO_PI	  = 0x1000
	CIFF_ONE_QUEUE	  = 0x2000
)

var (
	native		binary.ByteOrder
	nextSeqNr	uint32
	ipAllocator	= ipallocator.New()
	bridgeIPv4Net	*net.IPNet
	bridgeIface	string
	tapFile		*os.File
)

type ifReq struct {
	Name [IFNAMSIZ] byte
	Flags uint16
	pad [0x28 - 0x10 - 2]byte
}

type Settings struct {
	IPAddress		string
	IPPrefixLen		int
	Gateway			string
	Bridge			string
	Device			string
	File			*os.File
}

type IfInfomsg struct {
	syscall.IfInfomsg
}

type IfAddrmsg struct {
	syscall.IfAddrmsg
}

type ifreqIndex struct {
	IfrnName  [IFNAMSIZ]byte
	IfruIndex int32
}

type NetlinkRequestData interface {
	Len() int
	ToWireFormat() []byte
}

type IfAddr struct {
	iface	*net.Interface
	ip	net.IP
	ipNet	*net.IPNet
}

type RtAttr struct {
	syscall.RtAttr
	Data     []byte
	children []NetlinkRequestData
}

type NetlinkSocket struct {
	fd  int
	lsa syscall.SockaddrNetlink
}

type NetlinkRequest struct {
	syscall.NlMsghdr
	Data []NetlinkRequestData
}

// Network interface represents the networking stack of a container
type networkInterface struct {
	IP           net.IP
	PortMappings []net.Addr // There are mappings to the host interfaces
}

type ifaces struct {
	c map[string]*networkInterface
	sync.Mutex
}

func init() {
	var x uint32 = 0x01020304
	if *(*byte)(unsafe.Pointer(&x)) == 0x01 {
		native = binary.BigEndian
	} else {
		native = binary.LittleEndian
	}
}

func InitNetwork(bIface, bridgeIP string) error {
	if bIface == "" {
		bridgeIface = DefaultNetworkBridge
	} else {
		bridgeIface = bIface
	}

	addr, err := GetIfaceAddr(bridgeIface)

	if err != nil {
		glog.V(1).Info("create bridge %s %s\n", bridgeIface, bridgeIP)
		// No Bridge existent, create one

		// If the iface is not found, try to create it
		if err := configureBridge(bridgeIP, bridgeIface); err != nil {
			glog.Error("create bridge failed\n")
			return err
		}

		addr, err = GetIfaceAddr(bridgeIface)
		if err != nil {
			glog.Error("get iface addr failed\n")
			return err
		}

		bridgeIPv4Net = addr.(*net.IPNet);
	} else {
		glog.V(1).Info("bridge exist\n")
		// Validate that the bridge ip matches the ip specified by BridgeIP
		bridgeIPv4Net = addr.(*net.IPNet);

		if bridgeIP != "" {
			bip, _, err := net.ParseCIDR(bridgeIP)
			if err != nil {
				return err
			}
			if !bridgeIPv4Net.IP.Equal(bip) {
				return fmt.Errorf("Bridge ip (%s) does not match existing bridge configuration %s", addr, bip)
			}
		}
	}

	ipAllocator.RequestIP(bridgeIPv4Net, bridgeIPv4Net.IP);
	return nil
}

// Return the first IPv4 address for the specified network interface
func GetIfaceAddr(name string) (net.Addr, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var addr4 []net.Addr
	for _, addr := range addrs {
		ip := (addr.(*net.IPNet)).IP
		if ip4 := ip.To4(); ip4 != nil {
			addr4 = append(addr4, addr)
		}
	}

	if len(addr4) == 0 {
		return nil, fmt.Errorf("Interface %v has no IPv4 addresses", name)
	}
	return addr4[0], nil
}

// create and setup network bridge
func configureBridge(bridgeIP, bridgeIface string) error {
	var ifaceAddr string
	if len(bridgeIP) != 0 {
		_, _, err := net.ParseCIDR(bridgeIP)
		if err != nil {
			glog.Error("%s parsecidr failed\n", bridgeIP)
			return err
		}
		ifaceAddr = bridgeIP
	}

	if ifaceAddr == "" {
		return fmt.Errorf("Could not find a free IP address range for interface '%s'. Please configure its address manually", bridgeIface, bridgeIface)
	}

	if err := CreateBridgeIface(bridgeIface); err != nil {
		// The bridge may already exist, therefore we can ignore an "exists" error
		if !os.IsExist(err) {
			glog.Error("CreateBridgeIface failed %s %s\n", bridgeIface, ifaceAddr)
			return err
		}
	}

	iface, err := net.InterfaceByName(bridgeIface)
	if err != nil {
		return err
	}

	ipAddr, ipNet, err := net.ParseCIDR(ifaceAddr)
	if err != nil {
		return err
	}

	if err := NetworkLinkAddIp(iface, ipAddr, ipNet); err != nil {
		return fmt.Errorf("Unable to add private network: %s", err)
	}

	if err := NetworkLinkUp(iface); err != nil {
		return fmt.Errorf("Unable to start network bridge: %s", err)
	}
	return nil
}

func getNetlinkSocket() (*NetlinkSocket, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	s := &NetlinkSocket{
		fd: fd,
	}
	s.lsa.Family = syscall.AF_NETLINK
	if err := syscall.Bind(fd, &s.lsa); err != nil {
		syscall.Close(fd)
		return nil, err
	}

	return s, nil
}

func (s *NetlinkSocket) Close() {
	syscall.Close(s.fd)
}

func (s *NetlinkSocket) Send(request *NetlinkRequest) error {
	if err := syscall.Sendto(s.fd, request.ToWireFormat(), 0, &s.lsa); err != nil {
		return err
	}
	return nil
}

func (s *NetlinkSocket) Receive() ([]syscall.NetlinkMessage, error) {
	rb := make([]byte, syscall.Getpagesize())
	nr, _, err := syscall.Recvfrom(s.fd, rb, 0)
	if err != nil {
		return nil, err
	}
	if nr < syscall.NLMSG_HDRLEN {
		return nil, fmt.Errorf("Got short response fromnetlink")
	}
	rb = rb[:nr]
	return syscall.ParseNetlinkMessage(rb)
}

func (s *NetlinkSocket) CheckMessage(m syscall.NetlinkMessage, seq, pid uint32) error {
	if m.Header.Seq != seq {
		return fmt.Errorf("netlink: invalid seq %d, expected %d", m.Header.Seq, seq)
	}
	if m.Header.Pid != pid {
		return fmt.Errorf("netlink: wrong pid %d, expected %d", m.Header.Pid, pid)
	}
	if m.Header.Type == syscall.NLMSG_DONE {
		return io.EOF
	}
	if m.Header.Type == syscall.NLMSG_ERROR {
		e := int32(native.Uint32(m.Data[0:4]))
		if e == 0 {
			return io.EOF
		}
		return syscall.Errno(-e)
	}
	return nil
}

func (s *NetlinkSocket) GetPid() (uint32, error) {
	lsa, err := syscall.Getsockname(s.fd)
	if err != nil {
		return 0, err
	}
	switch v := lsa.(type) {
	case *syscall.SockaddrNetlink:
		return v.Pid, nil
	}
	return 0, fmt.Errorf("Wrong socket type")
}

func (s *NetlinkSocket) HandleAck(seq uint32) error {
	pid, err := s.GetPid()
	if err != nil {
		return err
	}

outer:
	for {
		msgs, err := s.Receive()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if err := s.CheckMessage(m, seq, pid); err != nil {
				if err == io.EOF {
					break outer
				}
				return err
			}
		}
	}

	return nil
}

func newIfInfomsg(family int) *IfInfomsg {
	return &IfInfomsg{
		IfInfomsg: syscall.IfInfomsg{
			Family: uint8(family),
		},
	}
}

func newIfInfomsgChild(parent *RtAttr, family int) *IfInfomsg {
	msg := newIfInfomsg(family)
	parent.children = append(parent.children, msg)
	return msg
}

func (msg *IfInfomsg) ToWireFormat() []byte {
	length := syscall.SizeofIfInfomsg
	b := make([]byte, length)
	b[0] = msg.Family
	b[1] = 0
	native.PutUint16(b[2:4], msg.Type)
	native.PutUint32(b[4:8], uint32(msg.Index))
	native.PutUint32(b[8:12], msg.Flags)
	native.PutUint32(b[12:16], msg.Change)
	return b
}

func (msg *IfInfomsg) Len() int {
	return syscall.SizeofIfInfomsg
}

func newIfAddrmsg(family int) *IfAddrmsg {
	return &IfAddrmsg{
		IfAddrmsg: syscall.IfAddrmsg{
			Family: uint8(family),
		},
	}
}

func (msg *IfAddrmsg) ToWireFormat() []byte {

	length := syscall.SizeofIfAddrmsg
	glog.V(1).Info("ifaddmsg length %d\n", length)
	b := make([]byte, length)
	b[0] = msg.Family
	b[1] = msg.Prefixlen
	b[2] = msg.Flags
	b[3] = msg.Scope
	native.PutUint32(b[4:8], uint32(msg.Index))
	return b
}

func (msg *IfAddrmsg) Len() int {
	return syscall.SizeofIfAddrmsg
}

func newRtAttr(attrType int, data []byte) *RtAttr {
	return &RtAttr{
		RtAttr: syscall.RtAttr{
			Type: uint16(attrType),
		},
		children: []NetlinkRequestData{},
		Data:     data,
	}
}

func rtaAlignOf(attrlen int) int {
	return (attrlen + syscall.RTA_ALIGNTO - 1) & ^(syscall.RTA_ALIGNTO - 1)
}

func (a *RtAttr) Len() int {
	if len(a.children) == 0 {
		return (syscall.SizeofRtAttr + len(a.Data))
	}

	l := 0
	for _, child := range a.children {
		l += child.Len()
	}
	l += syscall.SizeofRtAttr
	return rtaAlignOf(l + len(a.Data))
}

func (a *RtAttr) ToWireFormat() []byte {
	length := a.Len()
	buf := make([]byte, rtaAlignOf(length))

	if a.Data != nil {
		copy(buf[4:], a.Data)
	} else {
		next := 4
		for _, child := range a.children {
			childBuf := child.ToWireFormat()
			copy(buf[next:], childBuf)
			next += rtaAlignOf(len(childBuf))
		}
	}

	if l := uint16(length); l != 0 {
		native.PutUint16(buf[0:2], l)
	}
	native.PutUint16(buf[2:4], a.Type)
	return buf
}

func (rr *NetlinkRequest) ToWireFormat() []byte {
	length := rr.Len
	dataBytes := make([][]byte, len(rr.Data))
	for i, data := range rr.Data {
		dataBytes[i] = data.ToWireFormat()
		length += uint32(len(dataBytes[i]))
	}
	b := make([]byte, length)
	native.PutUint32(b[0:4], length)
	native.PutUint16(b[4:6], rr.Type)
	native.PutUint16(b[6:8], rr.Flags)
	native.PutUint32(b[8:12], rr.Seq)
	native.PutUint32(b[12:16], rr.Pid)

	next := 16
	for _, data := range dataBytes {
		copy(b[next:], data)
		next += len(data)
	}
	return b
}

func (rr *NetlinkRequest) AddData(data NetlinkRequestData) {
	if data != nil {
		rr.Data = append(rr.Data, data)
	}
}

func newNetlinkRequest(proto, flags int) *NetlinkRequest {
	return &NetlinkRequest{
		NlMsghdr: syscall.NlMsghdr{
			Len:   uint32(syscall.NLMSG_HDRLEN),
			Type:  uint16(proto),
			Flags: syscall.NLM_F_REQUEST | uint16(flags),
			Seq:   atomic.AddUint32(&nextSeqNr, 1),
		},
	}
}

func getIpFamily(ip net.IP) int {
	if len(ip) <= net.IPv4len {
		return syscall.AF_INET
	}
	if ip.To4() != nil {
		return syscall.AF_INET
	}
	return syscall.AF_INET6
}

func networkLinkIpAction(action, flags int, ifa IfAddr) error {
	s, err := getNetlinkSocket()
	if err != nil {
		return err
	}
	defer s.Close()

	family := getIpFamily(ifa.ip)

	nlreq := newNetlinkRequest(action, flags)

	msg := newIfAddrmsg(family)
	msg.Index = uint32(ifa.iface.Index)
	prefixLen, _ := ifa.ipNet.Mask.Size()
	msg.Prefixlen = uint8(prefixLen)
	nlreq.AddData(msg)

	var ipData []byte
	ipData = ifa.ip.To4()

	localData := newRtAttr(syscall.IFA_LOCAL, ipData)
	nlreq.AddData(localData)

	if err := s.Send(nlreq); err != nil {
		return err
	}

	return s.HandleAck(nlreq.Seq)
}

// Delete an IP address from an interface. This is identical to:
// ip addr del $ip/$ipNet dev $iface
func NetworkLinkDelIp(iface *net.Interface, ip net.IP, ipNet *net.IPNet) error {
	return networkLinkIpAction(
		syscall.RTM_DELADDR,
		syscall.NLM_F_ACK,
		IfAddr{iface, ip, ipNet},
	)
}

func NetworkLinkAddIp(iface *net.Interface, ip net.IP, ipNet *net.IPNet) error {
	return networkLinkIpAction(
		syscall.RTM_NEWADDR,
		syscall.NLM_F_CREATE|syscall.NLM_F_EXCL|syscall.NLM_F_ACK,
		IfAddr{iface, ip, ipNet},
	)
}

// Bring up a particular network interface.
// This is identical to running: ip link set dev $name up
func NetworkLinkUp(iface *net.Interface) error {
	s, err := getNetlinkSocket()
	if err != nil {
		return err
	}
	defer s.Close()

	nlreq := newNetlinkRequest(syscall.RTM_NEWLINK, syscall.NLM_F_ACK)

	msg := newIfInfomsg(syscall.AF_UNSPEC)
	msg.Index = int32(iface.Index)
	msg.Flags = syscall.IFF_UP
	msg.Change = syscall.IFF_UP
	nlreq.AddData(msg)

	if err := s.Send(nlreq); err != nil {
		return err
	}

	return s.HandleAck(nlreq.Seq)
}

// Bring down a particular network interface.
// This is identical to running: ip link set $name down
func NetworkLinkDown(iface *net.Interface) error {
	s, err := getNetlinkSocket()
	if err != nil {
		return err
	}
	defer s.Close()

	wb := newNetlinkRequest(syscall.RTM_NEWLINK, syscall.NLM_F_ACK)

	msg := newIfInfomsg(syscall.AF_UNSPEC)
	msg.Index = int32(iface.Index)
	msg.Flags = 0 & ^syscall.IFF_UP
	msg.Change = DEFAULT_CHANGE
	wb.AddData(msg)

	if err := s.Send(wb); err != nil {
		return err
	}

	return s.HandleAck(wb.Seq)
}

// THIS CODE DOES NOT COMMUNICATE WITH KERNEL VIA RTNETLINK INTERFACE
// IT IS HERE FOR BACKWARDS COMPATIBILITY WITH OLDER LINUX KERNELS
// WHICH SHIP WITH OLDER NOT ENTIRELY FUNCTIONAL VERSION OF NETLINK
func getIfSocket() (fd int, err error) {
	for _, socket := range []int{
		syscall.AF_INET,
		syscall.AF_PACKET,
		syscall.AF_INET6,
	} {
		if fd, err = syscall.Socket(socket, syscall.SOCK_DGRAM, 0); err == nil {
			break
		}
	}
	if err == nil {
		return fd, nil
	}
	return -1, err
}

// Create the actual bridge device.  This is more backward-compatible than
// netlink and works on RHEL 6.
func CreateBridgeIface(name string) error {
	if len(name) >= IFNAMSIZ {
		return fmt.Errorf("Interface name %s too long", name)
	}

	s, err := getIfSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(s)

	nameBytePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(s), SIOC_BRADDBR, uintptr(unsafe.Pointer(nameBytePtr))); err != 0 {
		return err
	}
	return nil
}

func DeleteBridge(name string) error {
	s, err := getIfSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(s)

	nameBytePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}

	var ifr ifReq
	copy(ifr.Name[:len(ifr.Name)-1], []byte(name))
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(s),
		syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr))); err != 0 {
		return err
	}

	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(s),
		SIOC_BRDELBR, uintptr(unsafe.Pointer(nameBytePtr))); err != 0 {
		return err
	}
	return nil
}

func AddToBridge(iface, master *net.Interface) error {
	if len(master.Name) >= IFNAMSIZ {
		return fmt.Errorf("Interface name %s too long", master.Name)
	}

	s, err := getIfSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(s)

	ifr := ifreqIndex{}
	copy(ifr.IfrnName[:len(ifr.IfrnName)-1], master.Name)
	ifr.IfruIndex = int32(iface.Index)

	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(s), SIOC_BRADDIF, uintptr(unsafe.Pointer(&ifr))); err != 0 {
		return err
	}

	return nil
}

func Allocate(requestedIP string) (*Settings, error) {
	var (
		req ifReq
		errno syscall.Errno
	)

	ip, err := ipAllocator.RequestIP(bridgeIPv4Net, net.ParseIP(requestedIP))
	if err != nil {
		return nil, err
	}

	maskSize, _ := bridgeIPv4Net.Mask.Size()

	tapFile, err = os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	req.Flags = CIFF_TAP
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, tapFile.Fd(),
				      uintptr(syscall.TUNSETIFF),
				      uintptr(unsafe.Pointer(&req)))
	if errno != 0 {
		err = fmt.Errorf("create tap device failed\n")
		tapFile.Close()
		return nil, err
	}

	device := strings.Trim(string(req.Name[:]), "\x00")

	tapIface, err := net.InterfaceByName(device)
	if err != nil {
		glog.Error("get interface by name %s failed %s\n", device, err)
		tapFile.Close()
		return nil, err
	}

	bIface, err := net.InterfaceByName(bridgeIface)
	if err != nil {
		glog.Error("get interface by name %s failed\n", bridgeIface)
		tapFile.Close()
		return nil, err
	}

	err = AddToBridge(tapIface, bIface)
	if err != nil {
		glog.Error("Add to bridge failed %s %s\n", bridgeIface, device)
		tapFile.Close()
		return nil, err
	}

	networkSettings := &Settings{
		IPAddress:	ip.String(),
		Gateway:	bridgeIPv4Net.IP.String(),
		Bridge:		bridgeIface,
		IPPrefixLen:	maskSize,
		Device:		device,
		File:		tapFile,
	}

	return networkSettings, nil
}

// Release an interface for a select ip
func Release(releasedIP string, file *os.File) error {
	file.Close()
	if err := ipAllocator.ReleaseIP(bridgeIPv4Net, net.ParseIP(releasedIP)); err != nil {
		return err
	}

	return nil
}

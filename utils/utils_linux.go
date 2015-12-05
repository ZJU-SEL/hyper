// +build linux

package utils

import (
	"fmt"
	"github.com/hyperhq/runv/lib/glog"
	"net"
	"os/exec"
	"syscall"
)

var (
	MS_BIND uintptr = syscall.MS_BIND
)

func Mount(source string, target string, fstype string, flags uintptr, data string) error {
	if err := syscall.Mount(source, target, fstype, flags, data); err != nil {
		return fmt.Errorf("Mount: %s", err.Error())
	}
	return nil
}

func AddNFSShareDir(desAddr string) error {
	exportfsCmd := fmt.Sprintf("exportfs -o rw,sync,no_root_squash %s", desAddr)
	exportfsCommand := exec.Command("/bin/sh", "-c", exportfsCmd)
	output, err := exportfsCommand.Output()
	if err != nil {
		glog.Error(output)
		return err
	}
	return nil
}

func RemoveNFSShareDir(desAddr string) error {
	exportfsCmd := fmt.Sprintf("exportfs -u %s", desAddr)
	exportfsCommand := exec.Command("/bin/sh", "-c", exportfsCmd)
	output, err := exportfsCommand.Output()
	if err != nil {
		glog.Error(output)
		return err
	}
	return nil
}

func GetIPOfEth0() (string, error) {
	intf, err := net.InterfaceByName("eth0")
	if err != nil {
		return "", err
	}
	addrs, err := intf.Addrs()
	if err != nil {
		return "", err
	}
	//if len(addrs) > 1{
	//  return "", fmt.Errorf("ambiguous addrs of eth0 ", addrs)
	//}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("Get IP of eth0 error")
}

func MountNFSShareDir(sourAddr, desPath string) error {
	parms := fmt.Sprintf("mount -t %s %s %s", "nfs", sourAddr, desPath)
	cmd := exec.Command("/bin/sh", "-c", parms)
	_, err := cmd.Output()
	if err != nil {
		return err
	}
	return nil
	//return Mount(sourAddr, desPath, "nfs", syscall.MS_MGC_VAL, "discard")
}

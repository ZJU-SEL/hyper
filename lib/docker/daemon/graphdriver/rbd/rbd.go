// +build linux

package rbd

import (
	"encoding/json"
	"fmt"
	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"
	"github.com/docker/libcontainer/label"
	"github.com/hyperhq/hyper/lib/docker/pkg/parsers"
	"github.com/hyperhq/hyper/lib/docker/pkg/units"
	"github.com/hyperhq/runv/lib/glog"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

const (
	DefaultRadosConfigFile     = "/etc/ceph/ceph.conf"
	DefaultDockerBaseImageSize = 10 * 1024 * 1024 * 1024
	DefaultMetaObjectDataSize  = 256
)

type RbdMappingInfo struct {
	Pool     string `json:"pool"`
	Name     string `json:"name"`
	Snapshot string `json:"snap"`
	Device   string `json:"device"`
}

type DevInfo struct {
	Hash        string `json:"hash"`
	Device      string `json:"-"`
	Size        uint64 `json:"size"`
	BaseHash    string `json:"base_hash"` //for delete snapshot
	Initialized bool   `json:"initialized"`

	mountCount int        `json:"-"`
	mountPath  string     `json:"-"`
	lock       sync.Mutex `json:"-"`
}

type MetaData struct {
	Devices     map[string]*DevInfo `json:"Devices"`
	devicesLock sync.Mutex          `json:"-"` // Protects all read/writes to Devices map
}

type RbdSet struct {
	MetaData
	conn  *rados.Conn
	ioctx *rados.IOContext

	//Options
	dataPoolName  string
	imagePrefix   string
	snapPrefix    string
	metaPrefix    string
	baseImageName string
	baseImageSize uint64
	clientId      string
	configFile    string

	filesystem   string
	mountOptions string
	mkfsArgs     []string
}

func (devices *RbdSet) getRbdImageName(hash string) string {
	if hash == "" {
		return devices.imagePrefix + "_" + devices.baseImageName
	} else {
		return devices.imagePrefix + "_" + hash
	}
}

func (devices *RbdSet) getRbdSnapName(hash string) string {
	return devices.snapPrefix + "_" + hash
}

func (devices *RbdSet) getRbdMetaOid(hash string) string {
	if hash == "" {
		return devices.metaPrefix + "_" + devices.baseImageName
	} else {
		return devices.metaPrefix + "_" + hash
	}
}

func (devices *RbdSet) initRbdSet(doInit bool) error {
	if err := devices.conn.ReadConfigFile(devices.configFile); err != nil {
		glog.Errorf("Rdb read config file failed: %v", err)
		return err
	}

	if err := devices.conn.Connect(); err != nil {
		glog.Errorf("Rbd connect failed: %v", err)
		return err
	}
	if err := devices.setupPool(devices.dataPoolName); err != nil {
		devices.conn.Shutdown()
		return err
	}

	ioctx, err := devices.conn.OpenIOContext(devices.dataPoolName)
	if err != nil {
		glog.Errorf("Rbd open pool %s failed: %v", devices.dataPoolName, err)

		devices.conn.Shutdown()
		return err
	}

	devices.ioctx = ioctx

	// Setup the base image
	if doInit {
		glog.V(1).Infof("Rbd setup base image")
		if err := devices.setupBaseImage(); err != nil {
			glog.Errorf("Rbd error device setupBaseImage: %s", err)
			return err
		}
	}

	return nil
}

func (devices *RbdSet) setupPool(dockerPoolName string) error {
	isPoolExist := false
	poolList, err := devices.conn.ListPools()
	if err != nil {
		glog.Errorf("Get pool list failed: %v", err)
		return err
	}
	for _, poolName := range poolList {
		if poolName == dockerPoolName {
			isPoolExist = true
			break
		}
	}
	if !isPoolExist {
		if err := devices.conn.MakePool(dockerPoolName); err != nil {
			glog.Errorf("Create Rbd pool %s failed: %v", err)
			return err
		} else {
			glog.Infof("Create New Rbd pool %s success", dockerPoolName)
		}
	}
	return nil
}

func (devices *RbdSet) setupBaseImage() error {
	//check base image is exist
	oldInfo, err := devices.lookupDevice("")
	if err != nil {
		return err
	}

	// base image is exist
	if oldInfo != nil {
		if oldInfo.Initialized {
			return nil
		} else {
			glog.V(1).Infof("Removing uninitialized base image")
			if err = devices.deleteImage(oldInfo); err != nil {
				return err
			}
		}
	}

	// base image is not exist, create it
	baseName := devices.getRbdImageName("")
	glog.V(1).Infof("Create base rbd image %s", baseName)

	// create initial image
	_, err = rbd.Create(devices.ioctx, baseName, devices.baseImageSize, 16, rbd.RbdFeatureLayering)
	if err != nil {
		glog.Errorf("Rbd create image %s failed: %v", baseName, err)
		return err
	}

	// register it
	devInfo, err := devices.registerDevice("", "", devices.baseImageSize)

	// map it
	err = devices.mapImageToRbdDevice(devInfo)
	if err != nil {
		glog.Errorf("Rbd map image %s failed: %v", baseName, err)
		return err
	}

	// unmap it at last
	defer devices.unmapImageFromRbdDevice(devInfo)

	glog.V(1).Infof("Rbd map image %s to %s", baseName, devInfo.Device)

	// create filesystem
	if err = devices.createFilesystem(devInfo); err != nil {
		glog.Errorf("Rbd create filesystem for image %s failed: %v", baseName, err)
		return err
	}

	devInfo.Initialized = true
	if err = devices.saveMetadata(devInfo); err != nil {
		devInfo.Initialized = false
		return err
	}
	return nil
}

func (devices *RbdSet) createFilesystem(info *DevInfo) error {
	devname := info.Device

	args := []string{}
	for _, arg := range devices.mkfsArgs {
		args = append(args, arg)
	}

	args = append(args, devname)

	var err error
	switch devices.filesystem {
	case "xfs":
		err = exec.Command("mkfs.xfs", args...).Run()
	case "ext4":
		err = exec.Command("mkfs.ext4", append([]string{"-E", "nodiscard,lazy_itable_init=0,lazy_journal_init=0"}, args...)...).Run()
		if err != nil {
			err = exec.Command("mkfs.ext4", append([]string{"-E", "nodiscard,lazy_itable_init=0"}, args...)...).Run()
		}
		if err != nil {
			return err
		}
		err = exec.Command("tune2fs", append([]string{"-c", "-1", "-i", "0"}, devname)...).Run()
	default:
		err = fmt.Errorf("Unsupported filesystem type %s", devices.filesystem)
	}
	if err != nil {
		return err
	}

	return nil
}

func (devices *RbdSet) lookupDevice(hash string) (*DevInfo, error) {
	devices.devicesLock.Lock()
	defer devices.devicesLock.Unlock()
	info := devices.Devices[hash]

	if info == nil {
		info, err := devices.loadMetadata(hash)

		if info != nil {
			devices.Devices[hash] = info
		}
		return info, err
	}
	return info, nil
}

func (devices *RbdSet) registerDevice(hash, baseHash string, size uint64) (*DevInfo, error) {
	glog.V(1).Infof("registerDevice(%s)", hash)
	info := &DevInfo{
		Hash:        hash,
		Device:      "",
		Size:        size,
		BaseHash:    baseHash,
		Initialized: false,
	}

	devices.devicesLock.Lock()
	devices.Devices[hash] = info
	devices.devicesLock.Unlock()

	if err := devices.saveMetadata(info); err != nil {
		// Try to remove unused device
		devices.devicesLock.Lock()
		delete(devices.Devices, hash)
		devices.devicesLock.Unlock()
		return nil, err
	}

	return info, nil
}

func (devices *RbdSet) unRegisterDevice(info *DevInfo) error {
	devices.devicesLock.Lock()
	delete(devices.Devices, info.Hash)
	devices.devicesLock.Unlock()

	if err := devices.removeMetadata(info); err != nil {
		devices.devicesLock.Lock()
		devices.Devices[info.Hash] = info
		devices.devicesLock.Unlock()
		glog.Errorf("Error removing meta data: %s", err)
		return err
	}
	return nil
}

func (devices *RbdSet) removeMetadata(info *DevInfo) error {
	metaOid := devices.getRbdMetaOid(info.Hash)

	if err := devices.ioctx.Delete(metaOid); err != nil {
		return fmt.Errorf("Rbd removing metadata %s failed: %s", info.Hash, err)
	}
	return nil
}

func (devices *RbdSet) loadMetadata(hash string) (*DevInfo, error) {
	info := &DevInfo{Hash: hash}
	metaOid := devices.getRbdMetaOid(hash)

	data := make([]byte, DefaultMetaObjectDataSize)
	dataLen, err := devices.ioctx.Read(metaOid, data, 0)
	if err != nil {
		if err != rados.RadosErrorNotFound {
			glog.Errorf("Rdb read metadata %s failed: %v", metaOid, err)
			return nil, err
		}
		glog.Infof("Rbd read metadata %s not found", metaOid)
		// not found
		return nil, nil
	}

	jsonData := data[:dataLen]

	if err := json.Unmarshal(jsonData, &info); err != nil {
		return nil, err
	}

	return info, err
}

func (devices *RbdSet) saveMetadata(info *DevInfo) error {
	metaOid := devices.getRbdMetaOid(info.Hash)

	jsonData, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("Error encoding metadata to json: %s", err)
	}

	if err = devices.ioctx.WriteFull(metaOid, jsonData); err != nil {
		glog.Errorf("Rbd write metadata %s failed: %v", info.Hash, err)
		return err
	}

	return nil
}

func (devices *RbdSet) createImage(hash, baseHash string) error {
	var snapshot *rbd.Snapshot

	baseImgName := devices.getRbdImageName(baseHash)
	imgName := devices.getRbdImageName(hash)
	img := rbd.GetImage(devices.ioctx, baseImgName)

	// create snapshot for hash
	snapName := devices.getRbdSnapName(hash)

	if err := img.Open(snapName); err != nil {
		if err != rbd.RbdErrorNotFound {
			glog.Errorf("Rbd open image %s with snap %s failed: %v", baseImgName, snapName, err)
			return err
		}

		// open image without snapshot name
		if err = img.Open(); err != nil {
			glog.Errorf("Rbd open image %s failed: %v", baseImgName, err)
			return err
		}

		// create snapshot
		if snapshot, err = img.CreateSnapshot(snapName); err != nil {
			glog.Errorf("Rbd create snaphost %s failed: %v", snapName, err)
			img.Close()
			return err
		}

	} else {
		snapshot = img.GetSnapshot(snapName)
	}

	// open snaphost success
	defer img.Close()

	// protect snapshot
	if err := snapshot.Protect(); err != nil {
		glog.Errorf("Rbd protect snapshot %s failed: %v", snapName, err)
		return err
	}

	// clone image
	_, err := img.Clone(snapName, devices.ioctx, imgName, rbd.RbdFeatureLayering, 16)
	if err != nil {
		glog.Errorf("Rbd clone snapshot %s@%s to %s failed: %v", baseImgName, snapName, imgName, err)
		return err
	}

	return nil
}

func (devices *RbdSet) HasChildren(info *DevInfo) (bool, error) {
	info.lock.Lock()
	defer info.lock.Unlock()
	imgName := devices.getRbdImageName(info.Hash)
	img := rbd.GetImage(devices.ioctx, imgName)
	if imgs, err := img.GetSnapshotNames(); err != nil {
		return false, err
	} else {
		return imgs != nil, nil
	}
}

func (devices *RbdSet) deleteImage(info *DevInfo) error {
	var snapshot *rbd.Snapshot

	// remove image
	imgName := devices.getRbdImageName(info.Hash)
	img := rbd.GetImage(devices.ioctx, imgName)

	//FIXME workaround for share graph
	if err := img.Open(false); err != nil {
		glog.Errorf("Error open img : %s", imgName)
		return err
	}
	if imgs, err := img.GetSnapshotNames(); err == nil && imgs != nil {
		return nil
	} else if err != nil {
		glog.Errorf("image not open, %s", err.Error())
		return err
	}
	img.Close()
	if err := img.Remove(); err != nil {
		glog.Errorf("Rbd delete image %s failed: %v", imgName, err)
		return err
	}

	// hash's parent
	snapName := devices.getRbdSnapName(info.Hash)
	baseImgName := devices.getRbdImageName(info.BaseHash)
	parentImg := rbd.GetImage(devices.ioctx, baseImgName)
	if err := parentImg.Open(snapName); err != nil {
		glog.Errorf("Rbd open image %s with snap %s failed: %v", baseImgName, snapName, err)
		return err
	} else {
		snapshot = parentImg.GetSnapshot(snapName)
	}

	defer parentImg.Close()

	// unprotect snapshot
	if err := snapshot.Unprotect(); err != nil {
		glog.Errorf("Rbd unprotect snapshot %s failed: %v", snapName, err)
		return err
	}

	// remove snapshot
	if err := snapshot.Remove(); err != nil {
		glog.Errorf("Rbd remove snapshot %s failed: %v", snapName, err)
	}

	// unregister it
	if err := devices.unRegisterDevice(info); err != nil {
		return err
	}
	return nil
}

func (devices *RbdSet) imageIsMapped(devInfo *DevInfo) (bool, error) {
	// Older rbd binaries are not printing the device on mapping so
	// we have to discover it with showmapped.
	out, err := exec.Command("rbd", "showmapped", "--format", "json").Output()
	if err != nil {
		glog.Errorf("Rbd run rbd showmapped failed: %v", err)
		return false, err
	}

	pool := devices.dataPoolName
	imgName := devices.getRbdImageName(devInfo.Hash)

	mappingInfos := map[string]*RbdMappingInfo{}
	json.Unmarshal(out, &mappingInfos)

	for _, info := range mappingInfos {
		if info.Pool == pool && info.Name == imgName {
			if devInfo.Device == "" {
				devInfo.Device = info.Device
			} else {
				if devInfo.Device != info.Device {
					glog.Errorf("Rbd image %s is mapped to %s, but not same as expect %s", devInfo.Hash,
						info.Device, devInfo.Device)
					devInfo.Device = info.Device
				}
			}
			return true, nil
		}
	}
	return false, nil
}

func (devices *RbdSet) mapImageToRbdDevice(devInfo *DevInfo) error {
	if devInfo.Device != "" {
		return nil
	}

	pool := devices.dataPoolName
	imgName := devices.getRbdImageName(devInfo.Hash)

	_, err := exec.Command("rbd", "--pool", pool, "map", imgName).Output()
	if err != nil {
		return err
	}

	v, _ := devices.imageIsMapped(devInfo)
	if v == true {
		return nil
	} else {
		return fmt.Errorf("Unable map image %s", imgName)
	}
}

func (devices *RbdSet) unmapImageFromRbdDevice(devInfo *DevInfo) error {
	if devInfo.Device == "" {
		return nil
	}

	v, _ := devices.imageIsMapped(devInfo)
	if v == false {
		devInfo.Device = ""
		return nil
	}

	if err := exec.Command("rbd", "unmap", devInfo.Device).Run(); err != nil {
		return err
	}

	devInfo.Device = ""
	return nil
}

func (devices *RbdSet) AddDevice(hash, baseHash string) error {
	baseInfo, err := devices.lookupDevice(baseHash)
	if err != nil {
		return err
	}

	baseInfo.lock.Lock()
	defer baseInfo.lock.Unlock()

	if info, _ := devices.lookupDevice(hash); info != nil {
		//FIXME workaround for share image
		//		return fmt.Errorf("Rbd device %s already exists", hash)
		glog.V(1).Infof("[rbdset] Rbd device %s already exists----------", hash)
		return nil
	}

	glog.V(1).Infof("[rbdset] Create image hash %s baseHash %s", hash, baseHash)
	if err := devices.createImage(hash, baseHash); err != nil {
		glog.Errorf("Rdb error creating image %s (parent %s): %s", hash, baseHash, err)
	}

	if _, err := devices.registerDevice(hash, baseHash, baseInfo.Size); err != nil {
		//TODO: delete image
		glog.Errorf("Rbd register device failed: %v", err)
		return err
	}

	return nil
}

func (devices *RbdSet) DeleteDevice(hash string) error {
	info, err := devices.lookupDevice(hash)
	if err != nil {
		return err
	}

	info.lock.Lock()
	defer info.lock.Unlock()

	glog.V(1).Infof("[rbdset] Delete image %s", info.Hash)
	return devices.deleteImage(info)
}

func (devices *RbdSet) MountDevice(hash, mountPoint, mountLabel string) error {
	info, err := devices.lookupDevice(hash)
	if err != nil {
		return err
	}

	info.lock.Lock()
	defer info.lock.Unlock()

	if info.mountCount > 0 {
		if mountPoint != info.mountPath {
			return fmt.Errorf("Trying to mount rbd device in multple places (%s, %s)", info.mountPath, info.Device)
		}

		info.mountCount++
		return nil
	}

	glog.V(1).Infof("[rbdset] Mount image %s with device %s to %s", info.Hash, info.Device, info.mountPath)
	if err := devices.mapImageToRbdDevice(info); err != nil {
		return err
	}
	glog.V(1).Infof("[rbdset] Mount image to rbd device done")

	var flags uintptr = syscall.MS_MGC_VAL

	fstype, err := ProbeFsType(info.Device)
	if err != nil {
		return err
	}

	options := ""

	if fstype == "xfs" {
		// XFS needs nouuid or it can't mount filesystems with the same fs
		options = joinMountOptions(options, "nouuid")
	}

	options = joinMountOptions(options, devices.mountOptions)
	options = joinMountOptions(options, label.FormatMountLabel("", mountLabel))

	glog.V(1).Infof("[rbdset] Start mount rbd device to %s", info.mountPath)
	err = syscall.Mount(info.Device, mountPoint, fstype, flags, joinMountOptions("discard", options))
	if err != nil && err == syscall.EINVAL {
		err = syscall.Mount(info.Device, mountPoint, fstype, flags, options)
	}
	if err != nil {
		return fmt.Errorf("Error mounting '%s' on '%s': %s", info.Device, mountPoint, err)
	}
	glog.V(1).Infof("[rbdset] Mount done")

	info.mountCount = 1
	info.mountPath = mountPoint

	return nil
}

func (devices *RbdSet) UnmountDevice(hash string) error {
	info, err := devices.lookupDevice(hash)
	if err != nil {
		return err
	}

	info.lock.Lock()
	defer info.lock.Unlock()

	if info.mountCount == 0 {
		return fmt.Errorf("UnmountDevice: device not-mounted id %s\n", hash)
	}

	info.mountCount--
	if info.mountCount > 0 {
		return nil
	}

	glog.V(1).Infof("[rbdset] Unmount(%s)", info.mountPath)
	if err := syscall.Unmount(info.mountPath, 0); err != nil {
		return err
	}
	glog.V(1).Infof("[rbdset] Unmount done")

	info.mountPath = ""

	if err := devices.unmapImageFromRbdDevice(info); err != nil {
		return err
	}

	return nil
}

func (devices *RbdSet) HasDevice(hash string) bool {
	info, _ := devices.lookupDevice(hash)
	return info != nil
}

func (devices *RbdSet) Shutdown() error {

	glog.V(1).Infof("[rbdset %s] shutdown()", devices.imagePrefix)
	defer glog.V(1).Infof("[rbdset %s] shutdown END", devices.imagePrefix)

	var devs []*DevInfo

	devices.devicesLock.Lock()
	for _, info := range devices.Devices {
		devs = append(devs, info)
	}
	devices.devicesLock.Unlock()

	for _, info := range devs {
		info.lock.Lock()
		if info.mountCount > 0 {
			// We use MNT_DETACH here in case it is still busy in some running
			// container. This means it'll go away from the global scope directly,
			// and the device will be released when that container dies.
			if err := syscall.Unmount(info.mountPath, syscall.MNT_DETACH); err != nil {
				glog.V(1).Infof("Shutdown unmounting %s, error: %s", info.mountPath, err)
			}

			if err := devices.unmapImageFromRbdDevice(info); err != nil {
				glog.V(1).Infof("Shutdown unmap %s , error: %s", info.Hash, err)
			}
		}
		info.lock.Unlock()
	}

	info, _ := devices.lookupDevice("")
	if info != nil {
		info.lock.Lock()
		if err := devices.unmapImageFromRbdDevice(info); err != nil {
			glog.V(1).Infof("Shutdown unmap base , error: %s", err)
		}
		info.lock.Unlock()
	}

	//disconnect from rados
	glog.V(1).Infof("Disconnect from rados")
	devices.ioctx.Destroy()
	devices.conn.Shutdown()

	return nil
}

func NewRbdSet(root string, doInit bool, options []string) (*RbdSet, error) {
	conn, _ := rados.NewConn()
	devices := &RbdSet{
		MetaData:      MetaData{Devices: make(map[string]*DevInfo)},
		conn:          conn,
		dataPoolName:  "docker-image-pool",
		imagePrefix:   "docker_image",
		snapPrefix:    "docker_snap",
		metaPrefix:    "docker_meta",
		baseImageName: "base_image",
		baseImageSize: DefaultDockerBaseImageSize,
		clientId:      "admin",
		configFile:    DefaultRadosConfigFile,
		filesystem:    "ext4",
	}

	for _, option := range options {
		key, val, err := parsers.ParseKeyValueOpt(option)
		if err != nil {
			return nil, err
		}

		key = strings.ToLower(key)

		switch key {
		case "rbd.basesize":
			size, err := units.RAMInBytes(val)
			if err != nil {
				return nil, err
			}
			devices.baseImageSize = uint64(size)
		case "rbd.datapool":
			devices.dataPoolName = val
		case "rbd.imageprefix":
			devices.imagePrefix = val
		case "rbd.client":
			devices.clientId = val
		case "rbd.configfile":
			devices.configFile = val
		case "rbd.fs":
			if val != "ext4" && val != "xfs" {
				return nil, fmt.Errorf("Unsupported filesystem %s\n", val)
			}
			devices.filesystem = val
		case "rbd.mkfsarg":
			devices.mkfsArgs = append(devices.mkfsArgs, val)
		case "rbd.mountopt":
			devices.mountOptions = joinMountOptions(devices.mountOptions, val)
		default:
			return nil, fmt.Errorf("Unknown option %s\n", key)
		}
	}

	if err := devices.initRbdSet(doInit); err != nil {
		return nil, err
	}
	return devices, nil
}

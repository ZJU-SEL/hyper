// +build linux

package devicemapper

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"

	"github.com/hyperhq/hyper/storage"
	"github.com/hyperhq/runv/lib/glog"
)

type jsonMetadata struct {
	Device_id      int  `json:"device_id"`
	Size           int  `json:"size"`
	Transaction_id int  `json:"transaction_id"`
	Initialized    bool `json:"initialized"`
}

type jsonTsMetadata struct {
	Open_transaction_id int    `json:"open_transaction_id"`
	Device_hash         string `json:"device_hash"`
	Device_id           int    `json:"device_id"`
}

// For device mapper, we do not need to mount the container to sharedDir.
// All of we need to provide the block device name of container.
func MountContainerToSharedDir(containerId, sharedDir, devPrefix string) (string, error) {
	devFullName := fmt.Sprintf("/dev/mapper/%s-%s", devPrefix, containerId)
	return devFullName, nil
}

func CreateNewDevice(containerId, devPrefix, rootPath string) error {
	var metadataPath = fmt.Sprintf("%s/metadata/", rootPath)
	// Get device id from the metadata file
	idMetadataFile := path.Join(metadataPath, containerId)
	if _, err := os.Stat(idMetadataFile); err != nil && os.IsNotExist(err) {
		return err
	}
	jsonData, err := ioutil.ReadFile(idMetadataFile)
	if err != nil {
		return err
	}
	var dat jsonMetadata
	if err := json.Unmarshal(jsonData, &dat); err != nil {
		return err
	}
	deviceId := dat.Device_id
	deviceSize := dat.Size
	// Activate the device for that device ID
	devName := fmt.Sprintf("%s-%s", devPrefix, containerId)
	poolName := fmt.Sprintf("/dev/mapper/%s-pool", devPrefix)
	createDeviceCmd := fmt.Sprintf("dmsetup create %s --table \"0 %d thin %s %d\"", devName, deviceSize/512, poolName, deviceId)
	glog.V(1).Infof("*************the createDeviceCmd is %s", createDeviceCmd)
	createDeviceCommand := exec.Command("/bin/sh", "-c", createDeviceCmd)
	output, err := createDeviceCommand.Output()
	if err != nil {
		glog.Error(output)
		return err
	}
	return nil
}

func RestoreDevice(dmpool *DeviceMapper, containerId, devPrefix, rootPath string) error {
	var metadataPath = fmt.Sprintf("%s/metadata/", rootPath)
	// Get device id from the metadata file
	idMetadataFile := path.Join(metadataPath, containerId)
	if _, err := os.Stat(idMetadataFile); err != nil && os.IsNotExist(err) {
		return err
	}

	jsonData, err := ioutil.ReadFile(idMetadataFile)
	if err != nil {
		return err
	}
	//get old device data
	var dat jsonMetadata
	if err := json.Unmarshal(jsonData, &dat); err != nil {
		return err
	}
	deviceId := dat.Device_id
	deviceSize := dat.Size

	//create old device
	glog.V(1).Infof("Create temp old device to migrate image data")
	tmpDevName := dmpool.PoolName[:strings.Index(dmpool.PoolName, "-pool")] + "-device"
	parms := fmt.Sprintf("dmsetup create %s --table \"0 %d thin %s %d\"", tmpDevName, deviceSize/512, path.Join("/dev/mapper/", dmpool.PoolName), deviceId)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	//delete tmp device
	defer func() {
		glog.V(1).Infof("Remove tmp device")
		parms = fmt.Sprintf("dmsetup remove %s", tmpDevName)
		if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
			glog.Error(string(res))
		}
	}()

	//get data from transaction-metadata
	tsMetadataFile := path.Join(metadataPath, "transaction-metadata")
	if _, err := os.Stat(tsMetadataFile); err != nil && os.IsNotExist(err) {
		return err
	}
	jsonData, err = ioutil.ReadFile(tsMetadataFile)
	if err != nil {
		return err
	}
	var tsData jsonTsMetadata
	if err := json.Unmarshal(jsonData, &tsData); err != nil {
		return err
	}
	// FIXME Not Sure how to use transaction_id
	tsData.Open_transaction_id = tsData.Open_transaction_id + 1
	tsData.Device_id = tsData.Device_id + 1
	tsData.Device_hash = containerId

	//set new device data
	dat.Device_id = tsData.Device_id
	deviceId = dat.Device_id
	dat.Transaction_id = tsData.Open_transaction_id

	//create new volume
	glog.V(1).Infof("Create new device %d to hold migration container's image", deviceId)
	poolName := fmt.Sprintf("/dev/mapper/%s-pool", devPrefix)
	parms = fmt.Sprintf("dmsetup message %s 0 \"create_thin %d\"", poolName, deviceId)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	//create new snapshot device of old
	glog.V(1).Infof("Transaction data using external snapshot")
	devName := fmt.Sprintf("%s-%s", devPrefix, containerId)
	parms = fmt.Sprintf("dmsetup create %s --table \"0 %d thin %s %d %s\"", devName, deviceSize/512, poolName, deviceId, path.Join("/dev/mapper/", tmpDevName))
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	//write new data to file
	jsonData, err = json.Marshal(dat)
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(idMetadataFile, jsonData, 0600)
	if err != nil {
		return err
	}
	return nil
}

func InjectFile(src io.Reader, containerId, devPrefix, target, rootPath string, perm, uid, gid int) error {
	if containerId == "" {
		return fmt.Errorf("Please make sure the arguments are not NULL!\n")
	}
	permDir := perm | 0111
	// Define the basic directory, need to get them via the 'info' command
	var (
		mntPath = fmt.Sprintf("%s/mnt/", rootPath)
		devName = fmt.Sprintf("%s-%s", devPrefix, containerId)
	)

	// Get the mount point for the container ID
	idMountPath := path.Join(mntPath, containerId)
	rootFs := path.Join(idMountPath, "rootfs")
	targetFile := path.Join(rootFs, target)

	// Whether we have the mounter directory
	if _, err := os.Stat(idMountPath); err != nil && os.IsNotExist(err) {
		if err := os.MkdirAll(idMountPath, os.FileMode(permDir)); err != nil {
			return err
		}
	}

	// Mount the block device to that mount point
	var flags uintptr = syscall.MS_MGC_VAL
	devFullName := fmt.Sprintf("/dev/mapper/%s", devName)
	fstype, err := ProbeFsType(devFullName)
	if err != nil {
		return err
	}
	glog.V(3).Infof("The filesytem type is %s", fstype)
	options := ""
	if fstype == "xfs" {
		// XFS needs nouuid or it can't mount filesystems with the same fs
		options = joinMountOptions(options, "nouuid")
	}

	err = syscall.Mount(devFullName, idMountPath, fstype, flags, joinMountOptions("discard", options))
	if err != nil && err == syscall.EINVAL {
		err = syscall.Mount(devFullName, idMountPath, fstype, flags, options)
	}
	if err != nil {
		return fmt.Errorf("Error mounting '%s' on '%s': %s", devFullName, idMountPath, err)
	}
	defer syscall.Unmount(idMountPath, syscall.MNT_DETACH)

	return storage.WriteFile(src, targetFile, perm, uid, gid)
}

func ProbeFsType(device string) (string, error) {
	// The daemon will only be run on Linux platform,  so 'file -s' command
	// will be used to test the type of filesystem which the device located.
	cmd := fmt.Sprintf("file -sL %s", device)
	command := exec.Command("/bin/sh", "-c", cmd)
	fileCmdOutput, err := command.Output()
	if err != nil {
		return "", nil
	}

	if strings.Contains(strings.ToLower(string(fileCmdOutput)), "ext") {
		return "ext4", nil
	}
	if strings.Contains(strings.ToLower(string(fileCmdOutput)), "xfs") {
		return "xfs", nil
	}

	return "", fmt.Errorf("Unknown filesystem type on %s", device)
}

func joinMountOptions(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "," + b
}

type DeviceMapper struct {
	Datafile         string
	Metadatafile     string
	DataLoopFile     string
	MetadataLoopFile string
	Size             int
	PoolName         string
}

func CreatePool(dm *DeviceMapper) error {
	if _, err := os.Stat("/dev/mapper/" + dm.PoolName); err == nil {
		return nil
	}
	// Create data file and metadata file
	parms := fmt.Sprintf("dd if=/dev/zero of=%s bs=1 seek=%d count=0", dm.Datafile, dm.Size)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	parms = fmt.Sprintf("fallocate -l 128M %s", dm.Metadatafile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}

	if _, err := os.Stat(dm.DataLoopFile); err != nil {
		l := len(dm.DataLoopFile)
		parms = fmt.Sprintf("mknod -m 0660 %s b 7 %s", dm.DataLoopFile, dm.DataLoopFile[(l-1):l])
		if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
			glog.Error(string(res))
			return fmt.Errorf(string(res))
		}
	}
	if _, err := os.Stat(dm.MetadataLoopFile); err != nil {
		l := len(dm.MetadataLoopFile)
		parms = fmt.Sprintf("mknod -m 0660 %s b 7 %s", dm.MetadataLoopFile, dm.MetadataLoopFile[(l-1):l])
		if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
			glog.Error(string(res))
			return fmt.Errorf(string(res))
		}
	}
	// Setup the loop device for data and metadata files
	parms = fmt.Sprintf("losetup %s %s", dm.DataLoopFile, dm.Datafile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}

	parms = fmt.Sprintf("losetup %s %s", dm.MetadataLoopFile, dm.Metadatafile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}

	// Make filesystem for data loop device and metadata loop device
	parms = fmt.Sprintf("mkfs.ext4 %s", dm.DataLoopFile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	parms = fmt.Sprintf("mkfs.ext4 %s", dm.MetadataLoopFile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	parms = fmt.Sprintf("dd if=/dev/zero of=%s bs=4096 count=1", dm.MetadataLoopFile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}

	parms = fmt.Sprintf("dmsetup create %s --table '0 %d thin-pool %s %s 128 0'", dm.PoolName, dm.Size/512, dm.MetadataLoopFile, dm.DataLoopFile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	return nil
}

func RestorePool(dm *DeviceMapper) error {
	// Setup the loop device for data and metadata files
	nextFreeLoop, err := getNextFreeDevice()
	if err != nil {
		return err
	}
	glog.V(1).Infof("Get free loopback device %s", nextFreeLoop)
	dm.DataLoopFile = nextFreeLoop
	err = setupLoopDevice(dm.DataLoopFile, dm.Datafile)
	if err != nil {
		return err
	}

	nextFreeLoop, err = getNextFreeDevice()
	if err != nil {
		return err
	}
	glog.V(1).Infof("Get free loopback device %s", nextFreeLoop)
	dm.MetadataLoopFile = nextFreeLoop
	err = setupLoopDevice(dm.MetadataLoopFile, dm.Metadatafile)
	if err != nil {
		return err
	}

	parms := fmt.Sprintf("dmsetup create %s --table '0 %d thin-pool %s %s 128 0'", dm.PoolName, dm.Size/512, dm.MetadataLoopFile, dm.DataLoopFile)
	glog.V(1).Infof("Create thin-pool command: %s", parms)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	return nil
}

func setupLoopDevice(loopFile, dataFile string) error {
	if _, err := os.Stat(loopFile); err != nil {
		l := len(loopFile)
		parms := fmt.Sprintf("mknod -m 0660 %s b 7 %s", loopFile, loopFile[(l-1):l])
		if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
			glog.Error(string(res))
			return fmt.Errorf(string(res))
		}
	}
	parms := fmt.Sprintf("losetup %s %s", loopFile, dataFile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	return nil
}

func getNextFreeDevice() (string, error) {
	parms := fmt.Sprintf("losetup -f")
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		return "", fmt.Errorf(string(res))
	} else {
		//delete \n
		result := string(res)
		return result[:strings.Index(result, "\n")], nil
	}
}

func CreateVolume(poolName, volName, dev_id string, size int, restore bool) error {
	glog.Infof("/dev/mapper/%s", volName)
	if _, err := os.Stat("/dev/mapper/" + volName); err == nil {
		return nil
	}
	if restore == false {
		parms := fmt.Sprintf("dmsetup message /dev/mapper/%s 0 \"create_thin %s\"", poolName, dev_id)
		if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
			glog.Error(string(res))
			return fmt.Errorf(string(res))
		}
	}
	parms := fmt.Sprintf("dmsetup create %s --table \"0 %d thin /dev/mapper/%s %s\"", volName, size/512, poolName, dev_id)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}

	if restore == false {
		parms = fmt.Sprintf("mkfs.ext4 \"/dev/mapper/%s\"", volName)
		if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
			glog.Error(string(res))
			return fmt.Errorf(string(res))
		}
	}
	return nil
}

func DeleteVolume(dm *DeviceMapper, dev_id int) error {
	var parms string
	// Delete the thin pool for test
	parms = fmt.Sprintf("dmsetup message /dev/mapper/%s 0 \"delete %d\"", dm.PoolName, dev_id)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	return nil
}

// Delete the pool which is created in 'Init' function
func DMCleanup(dm *DeviceMapper) error {
	var parms string
	// Delete the thin pool for test
	parms = fmt.Sprintf("dmsetup remove \"/dev/mapper/%s\"", dm.PoolName)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	// Delete the loop device
	parms = fmt.Sprintf("losetup -d %s", dm.MetadataLoopFile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	parms = fmt.Sprintf("losetup -d %s", dm.DataLoopFile)
	if res, err := exec.Command("/bin/sh", "-c", parms).CombinedOutput(); err != nil {
		glog.Error(string(res))
		return fmt.Errorf(string(res))
	}
	return nil
}

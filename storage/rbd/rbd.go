// +build linux

package rbd

import (
	"encoding/json"
	"fmt"
	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"
	"github.com/hyperhq/hyper/storage"
	"github.com/hyperhq/runv/lib/glog"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
)

type CephRbd struct {
	Conn  *rados.Conn
	Ioctx *rados.IOContext

	Order       int
	VolFsType   string
	ImageFsType string

	ConfigFile    string
	VolPoolName   string
	ImagePoolName string
	ClientId      string
	ImagePrefix   string
	VolPrefix     string
}

type rbdMappingInfo struct {
	Pool     string `json:"pool"`
	Name     string `json:"name"`
	Snapshot string `json:"snap"`
	Device   string `json:"device"`
}

func InitRbdPool(cr *CephRbd) error {
	conn, _ := rados.NewConn()
	cr.Conn = conn
	if err := cr.Conn.ReadConfigFile(cr.ConfigFile); err != nil {
		glog.Errorf("Rbd read config file failed: %v", err)
		return err
	}
	if err := cr.Conn.Connect(); err != nil {
		glog.Errorf("Rbd connect failed: %v", err)
		return err
	}

	isPoolExist := false
	poolList, err := cr.Conn.ListPools()
	if err != nil {
		glog.Errorf("Get pool list failed: %v", err)
		cr.Conn.Shutdown()
		return err
	}
	for _, poolName := range poolList {
		if poolName == cr.VolPoolName {
			isPoolExist = true
			break
		}
	}
	if !isPoolExist {
		if err := cr.Conn.MakePool(cr.VolPoolName); err != nil {
			glog.Errorf("Create Rbd pool %s failed: %v", cr.VolPoolName, err)
			cr.Conn.Shutdown()
			return err
		} else {
			glog.Infof("Create Rbd pool %s succeed", cr.VolPoolName)
		}
	}

	ioctx, err := cr.Conn.OpenIOContext(cr.VolPoolName)
	if err != nil {
		glog.Errorf("Rbd open pool %s failed: %v", cr.VolPoolName, err)
		cr.Conn.Shutdown()
		return err
	}
	cr.Ioctx = ioctx
	return nil
}

//get ceph rbd and map to local
func GetRemoteDevice(poolName, imgName string) error {
	return mapImageToRbdDevice(poolName, imgName)
}

func mapImageToRbdDevice(poolName, imgName string) error {
	if mapped, _ := isImageMapped(poolName, imgName); mapped == true {
		return nil
	}
	_, err := exec.Command("rbd", "--pool", poolName, "map", imgName).Output()
	if err != nil {
		glog.Errorf("exec map image %s failed: %v", imgName, err)
		return err
	}

	//Not sure why check again, same as orginal code
	mapped, _ := isImageMapped(poolName, imgName)
	if mapped {
		return nil
	} else {
		glog.Errorf("Unable map image %s", imgName)
		return fmt.Errorf("Unable map image %s", imgName)
	}
}

func unmapImageFromRbdDevice(poolName, imgName string) error {
	if mapped, _ := isImageMapped(poolName, imgName); mapped == false {
		return nil
	}

	fullDeviceName := fmt.Sprintf("/dev/rbd/%s/%s", poolName, imgName)
	if err := exec.Command("rbd", "unmap", fullDeviceName).Run(); err != nil {
		return err
	}
	return nil
}

func isImageMapped(poolName, imgName string) (bool, error) {
	out, err := exec.Command("rbd", "showmapped", "--format", "json").Output()
	if err != nil {
		glog.Errorf("Rbd run rbd showmapped failed: %v", err)
		return false, err
	}
	mappingInfos := map[string]*rbdMappingInfo{}
	json.Unmarshal(out, &mappingInfos)
	for _, info := range mappingInfos {
		if info.Pool == poolName && info.Name == imgName {
			return true, nil
		}
	}
	return false, nil
}

func MountContainerToShareDir(id, shareDir string, cr *CephRbd) (string, error) {
	devFullName := fmt.Sprintf("/dev/rbd/%s/%s_%s", cr.ImagePoolName, cr.ImagePrefix, id)
	return devFullName, nil
}

func ProbeFsType(device string) (string, error) {
	cmd := fmt.Sprintf("file -sL %s", device)
	command := exec.Command("/bin/sh", "-c", cmd)
	fileCmdOutput, err := command.Output()
	if err != nil {
		return "", err
	}

	if strings.Contains(strings.ToLower(string(fileCmdOutput)), "ext") {
		return "ext4", nil
	}
	if strings.Contains(strings.ToLower(string(fileCmdOutput)), "xfs") {
		return "xfs", nil
	}

	return "", fmt.Errorf("Unknown filesystem type on %s", device)
}

func InjectFile(src io.Reader, cr *CephRbd, containerId, target, rootPath string, perm, uid, gid int) error {
	if containerId == "" {
		return fmt.Errorf("Please make sure the arguments are not NULL!\n")
	}
	permDir := perm | 0111

	var (
		mntPath = fmt.Sprintf("%s/mnt/", rootPath)
		devName = fmt.Sprintf("%s_%s", cr.ImagePrefix, containerId)
	)

	idMountPath := path.Join(mntPath, containerId)
	rootFs := path.Join(idMountPath, "rootfs")
	targetFile := path.Join(rootFs, target)

	if _, err := os.Stat(idMountPath); err != nil && os.IsNotExist(err) {
		if err := os.MkdirAll(idMountPath, os.FileMode(permDir)); err != nil {
			return err
		}
	}

	var flags uintptr = syscall.MS_MGC_VAL
	devFullName := fmt.Sprintf("/dev/rbd/%s/%s", cr.ImagePoolName, devName)
	fstype, err := ProbeFsType(devFullName)
	if err != nil {
		return err
	}
	glog.V(3).Infof("The filesystem type is %s", fstype)
	options := ""
	if fstype == "xfs" {
		options = joinMountOptions(options, "nouuid")
	}

	err = syscall.Mount(devFullName, idMountPath, fstype, flags, joinMountOptions("discard", options))
	if err != nil && err == syscall.EINVAL {
		err = syscall.Mount(devFullName, idMountPath, fstype, flags, options)
	}
	if err != nil {
		return fmt.Errorf("Error mounting '%s', on '%s'", devFullName, idMountPath, err)
	}
	defer syscall.Unmount(idMountPath, syscall.MNT_DETACH)

	return storage.WriteFile(src, targetFile, perm, uid, gid)
}

func CreateVolume(cr *CephRbd, volName string, size int, restore bool) error {
	fullDeviceName := fmt.Sprintf("/dev/rbd/%s/%s", cr.VolPoolName, volName)
	glog.Infof(fullDeviceName)
	//check whether device is already mapped
	if _, err := os.Stat(fullDeviceName); err == nil {
		return nil
	}

	if !restore {
		//create new vol image
		_, err := rbd.Create(cr.Ioctx, volName, uint64(size), cr.Order, rbd.RbdFeatureLayering)
		if err != nil {
			glog.Errorf("Rbd create image %s failed: %v", volName, err)
			return err
		}
	}

	if err := mapImageToRbdDevice(cr.VolPoolName, volName); err != nil {
		return err
	}

	if !restore {
		//create filesystem
		if err := createFilesystem(fullDeviceName, cr.VolFsType, []string{}); err != nil {
			glog.Errorf("Create vol filesystem failed: %v", err)
			return err
		}
	}
	return nil
}

func DeleteVolume(cr *CephRbd, volName string) error {
	volImage := rbd.GetImage(cr.Ioctx, volName)
	if err := volImage.Remove(); err != nil {
		glog.Errorf("Rbd delete volume image %s failed: %v", volName, err)
		return err
	}
	return nil
}

func CRCleanup(cr *CephRbd) error {
	glog.V(1).Infof("[Ceph CRCleanup]Don't need to clean up")
	return nil
}

func createFilesystem(fullDeviceName, fstype string, mkfsArgs []string) error {
	args := []string{}
	for _, arg := range mkfsArgs {
		args = append(args, arg)
	}

	args = append(args, fullDeviceName)

	var err error
	switch fstype {
	case "xfs":
		err = exec.Command("mkfs.xfs", args...).Run()
	case "ext4":
		err = exec.Command("mkfs.ext4", append([]string{"-E", "nodiscard,lazy_itable_init=0,lazy_journal_init=0"}, args...)...).Run()
		if err != nil {
			err = exec.Command("mkfs.ext4", append([]string{"-E", "nodiscard,lazy_itable_init"}, args...)...).Run()
		}
		if err != nil {
			return err
		}
		err = exec.Command("tune2fs", append([]string{"-c", "-1", "-i", "0"}, fullDeviceName)...).Run()
	default:
		err = fmt.Errorf("Unsupported filesystem type %s", fstype)
	}
	if err != nil {
		return err
	}
	return nil
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

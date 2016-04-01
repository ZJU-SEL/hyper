// +build !linux

package rbd

import (
    "fmt"
    "os/exec"
    "strings"
)

type CephRbd struct {
    Conn *rados.Conn
    Ioctx *rados.IOContext

    Order int
    VolFsType string
    ImageFsType string

    ConfigFile string
    VolPoolName string
    ImagePoolName string
    ClientId string
    ImagePrefix string
    VolPrefix string
}

func InitRbdPool(cr *CephRbd) error {
    return nil
}

func GetRemoteDevice(cr *CephRbd, id string) error{
    return nil
}

func MountContainerToShareDir(id, shareDir string, cr *CephRbd) (string, error){
    return "", nil
}

func ProbeFsType(device string) (string, error){
    cmd := fmt.Sprintf("file -sL %s", device)
    command := exec.Command("/bin/sh", "-c", cmd)
    fileCmdOutput, err := command.Output()
    if err != nil{
        return "", err
    }

    if strings.Contains(strings.ToLower(string(fileCmdOutput)), "ext"){
        return "ext4", nil
    }
    if strings.Contains(strings.ToLower(string(fileCmdOutput)), "xfs"){
        return "xfs", nil
    }

    return "", fmt.Errorf("Unknown filesystem type on %s", device)
}

func InjectFile(src io.Reader, cr *CephRbd, containerId, target, rootPath string, perm, uid, gid int) error{
    return fmt.Errorf("Unsupported, inject file to %s is not supported in current arch", target)
}

func CreateVolume(cr *CephRbd, volName string, size int, restore bool) error {
    return nil
}

func DeleteVolume(cr *CephRbd, volName string) error {
    return nil
}

func CRCleanup(cr *CephRbd) error {
    return nil
}

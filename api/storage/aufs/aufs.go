package aufs

import (
	"fmt"
    "bufio"
    "io/ioutil"
    "os"
	"os/exec"
    "path"
	"path/filepath"
    "syscall"
    "sync"
	"strconv"

	"dvm/lib/glog"
)

/*
|-- layers  // Metadata of layers
|   |---- 1
|   |---- 2
|   |---- 3
|-- diff    // Content of the layer
|   |---- 1
|   |---- 2
|   |---- 3
|-- mnt     // Mount points for the rw layers to be mounted
    |---- 1
    |---- 2
    |---- 3
*/

var (
    enableDirpermLock sync.Once
    enableDirperm     bool
)

const MsRemount = syscall.MS_REMOUNT

func MountContainerToSharedDir(containerId, rootDir, sharedDir, mountLabel string) (string, error) {
    var (
        //mntPath = path.Join(rootDir, "mnt")
        //layersPath = path.Join(rootDir, "layers")
        diffPath = path.Join(rootDir, "diff")
        mountPoint = path.Join(sharedDir, containerId, "rootfs")
    )

	layers, err := getParentDiffPaths(containerId, rootDir)
	if err != nil {
		return "", err
	}

	if err := aufsMount(layers, path.Join(diffPath, containerId), mountPoint, mountLabel); err != nil {
		return "", fmt.Errorf("DVM ERROR: error creating aufs mount to %s: %v", mountPoint, err)
	}

    return mountPoint, nil
}

func AttachFiles(containerId, fromFile, toDir, rootDir, perm string) error {
	if containerId == "" {
		return fmt.Errorf("Please make sure the arguments are not NULL!\n")
	}
	permInt, err := strconv.Atoi(perm)
	if err != nil {
		return err
	}
	// It just need the block device without copying any files
	// FIXME whether we need to return an error if the target directory is null
	if toDir == "" {
		return nil
	}
	// Make a new file with the given premission and wirte the source file content in it
	if _, err := os.Stat(fromFile); err != nil && os.IsNotExist(err) {
		return err
	}
	buf, err := ioutil.ReadFile(fromFile)
	if err != nil {
		return err
	}
	targetDir := path.Join(rootDir, containerId, "rootfs", toDir)
	targetInfo, err := os.Stat(targetDir)
	targetFile := targetDir
	if err != nil && os.IsNotExist(err) {
		if targetInfo.IsDir() {
			// we need to create a target directory with given premission
			if err := os.MkdirAll(targetDir, os.FileMode(permInt)); err != nil {
				return err
			}
			targetFile = path.Join(targetDir, filepath.Base(fromFile))
		} else {
			tmpDir := filepath.Dir(targetDir)
			if _, err := os.Stat(tmpDir); err != nil && os.IsNotExist(err) {
				if err := os.MkdirAll(tmpDir, os.FileMode(permInt)); err != nil {
					return err
				}
			}
		}
	} else {
		targetFile = path.Join(targetDir, filepath.Base(fromFile))
	}
	err = ioutil.WriteFile(targetFile, buf, os.FileMode(permInt))
	if err != nil {
		return err
	}

	return nil
}

func getParentDiffPaths(id, rootPath string) ([]string, error) {
	parentIds, err := getParentIds(path.Join(rootPath, "layers", id))
	if err != nil {
		return nil, err
	}
	layers := make([]string, len(parentIds))

	// Get the diff paths for all the parent ids
	for i, p := range parentIds {
		layers[i] = path.Join(rootPath, "diff", p)
	}
	return layers, nil
}

func aufsMount(ro []string, rw, target, mountLabel string) (err error) {
	defer func() {
		if err != nil {
			aufsUnmount(target)
		}
	}()

	// Mount options are clipped to page size(4096 bytes). If there are more
	// layers then these are remounted individually using append.

	offset := 54
	if useDirperm() {
		offset += len("dirperm1")
	}
	b := make([]byte, syscall.Getpagesize()-len(mountLabel)-offset) // room for xino & mountLabel
	bp := copy(b, fmt.Sprintf("br:%s=rw", rw))

	firstMount := true
	i := 0

	for {
		for ; i < len(ro); i++ {
			layer := fmt.Sprintf(":%s=ro+wh", ro[i])

			if firstMount {
				if bp+len(layer) > len(b) {
					break
				}
				bp += copy(b[bp:], layer)
			} else {
				data := FormatMountLabel(fmt.Sprintf("append%s", layer), mountLabel)
				if err = syscall.Mount("none", target, "aufs", MsRemount, data); err != nil {
					return
				}
			}
		}

		if firstMount {
			opts := "dio,xino=/dev/shm/aufs.xino"
			if useDirperm() {
				opts += ",dirperm1"
			}
			data := FormatMountLabel(fmt.Sprintf("%s,%s", string(b[:bp]), opts), mountLabel)
			if err = syscall.Mount("none", target, "aufs", 0, data); err != nil {
				return
			}
			firstMount = false
		}

		if i == len(ro) {
			break
		}
	}

	return
}

// FormatMountLabel returns a string to be used by the mount command.
// The format of this string will be used to alter the labeling of the mountpoint.
// The string returned is suitable to be used as the options field of the mount command.
// If you need to have additional mount point options, you can pass them in as
// the first parameter.  Second parameter is the label that you wish to apply
// to all content in the mount point.
func FormatMountLabel(src, mountLabel string) string {
        if mountLabel != "" {
                switch src {
                case "":
                        src = fmt.Sprintf("context=%q", mountLabel)
                default:
                        src = fmt.Sprintf("%s,context=%q", src, mountLabel)
                }
        }
        return src
}

// useDirperm checks dirperm1 mount option can be used with the current
// version of aufs.
func useDirperm() bool {
	enableDirpermLock.Do(func() {
		base, err := ioutil.TempDir("", "docker-aufs-base")
		if err != nil {
			glog.Errorf("error checking dirperm1: %s", err.Error())
			return
		}
		defer os.RemoveAll(base)

		union, err := ioutil.TempDir("", "docker-aufs-union")
		if err != nil {
			glog.Errorf("error checking dirperm1: %s", err.Error())
			return
		}
		defer os.RemoveAll(union)

		opts := fmt.Sprintf("br:%s,dirperm1,xino=/dev/shm/aufs.xino", base)
		if err := syscall.Mount("none", union, "aufs", 0, opts); err != nil {
			return
		}
		enableDirperm = true
		if err := aufsUnmount(union); err != nil {
			glog.Errorf("error checking dirperm1: failed to unmount %s", err.Error())
		}
	})
	return enableDirperm
}

func aufsUnmount(target string) error {
    if err := exec.Command("auplink", target, "flush").Run(); err != nil {
        glog.Errorf("Couldn't run auplink before unmount: %s", err.Error())
    }
    if err := syscall.Unmount(target, 0); err != nil {
        return err
    }
    return nil
}

// Return all the directories
func loadIds(root string) ([]string, error) {
    dirs, err := ioutil.ReadDir(root)
    if err != nil {
        return nil, err
    }
    out := []string{}
    for _, d := range dirs {
        if !d.IsDir() {
            out = append(out, d.Name())
        }
    }
    return out, nil
}

// Read the layers file for the current id and return all the
// layers represented by new lines in the file
//
// If there are no lines in the file then the id has no parent
// and an empty slice is returned.
func getParentIds(id string) ([]string, error) {
    f, err := os.Open(id)
    if err != nil {
        return nil, err
    }
    defer f.Close()

    out := []string{}
    s := bufio.NewScanner(f)

    for s.Scan() {
        if t := s.Text(); t != "" {
            out = append(out, s.Text())
        }
    }
    return out, s.Err()
}

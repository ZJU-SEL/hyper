package daemon

import (
	"fmt"
	"os"
	"path"

	dockertypes "github.com/docker/engine-api/types"
	"github.com/golang/glog"
	"github.com/hyperhq/hyper/utils"
	"github.com/hyperhq/runv/hypervisor"
	"github.com/hyperhq/runv/hypervisor/types"
)

func (daemon *Daemon) CleanPod(podId string) (int, string, error) {
	daemon.PodList.Lock()
	glog.V(2).Infof("lock PodList")
	defer glog.V(2).Infof("unlock PodList")
	defer daemon.PodList.Unlock()

	return daemon.CleanPodWithLock(podId)
}

func (daemon *Daemon) CleanPodWithLock(podId string) (int, string, error) {
	var (
		code  = 0
		cause = ""
		err   error
	)

	os.RemoveAll(path.Join(utils.HYPER_ROOT, "services", podId))
	os.RemoveAll(path.Join(utils.HYPER_ROOT, "hosts", podId))
	pod, ok := daemon.PodList.Get(podId)
	if !ok {
		return -1, "", fmt.Errorf("Can not find that Pod(%s)", podId)
	}

	if pod.status.Status == types.S_POD_RUNNING {
		code, cause, err = daemon.StopPodWithLock(podId, "yes")
		if err != nil {
			glog.Errorf("failed to stop pod %s", podId)
		}
	}

	daemon.DeletePodFromDB(podId)
	daemon.RemovePod(podId)
	if pod.status.Type != "kubernetes" {
		daemon.CleanUpContainer(pod.status)
	}
	daemon.DeleteVolumeId(podId)
	code = types.E_OK

	return code, cause, nil
}

func (daemon *Daemon) CleanUpContainer(ps *hypervisor.PodStatus) {
	for _, c := range ps.Containers {
		glog.V(1).Infof("Ready to rm container: %s", c.Id)
		if err := daemon.Daemon.ContainerRm(c.Id, &dockertypes.ContainerRmConfig{}); err != nil {
			glog.Warningf("Error to rm container: %s", err.Error())
		}
	}
	daemon.DeletePodContainerFromDB(ps.Id)
}

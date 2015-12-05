package daemon

import (
//	"fmt"
//	"github.com/hyperhq/hyper/engine"
///	"github.com/hyperhq/runv/hypervisor"
//	"github.com/hyperhq/runv/lib/glog"
)

/*
func (daemon *Daemon) CmdVmRestore(job *engine.Job) error {
	if len(job.Args) == 0 {
		return fmt.Errorf("Can not execute 'checkpoint' command without")
	}
	podId := job.Args[0]
	daemon.PodList.Lock()
	glog.V(2).Infof("lock PodList")
	defer glog.V(2).Infof("unlock PodList")
	defer daemon.PodList.Unlock()

	Pod, ok := daemon.PodList.Get(podId)
	if !ok {
		glog.Errorf("Can not find pod(%s)", podId)
		return fmt.Errorf("Can not find pod(%s)", podId)
	}
	vm := Pod.vm
	PodEvent, _, err := daemon.GetVmChan(vm)
	if err != nil {
		return err
	}
	migrateEvent := &hypervisor.RestoreVmCommand{}
	PodEvent <- migrateEvent

	v := &engine.Env{}
	v.Set("ID", podId)
	v.SetInt("Code", 0)
	v.Set("Cause", "")
	if _, err := v.WriteTo(job.Stdout); err != nil {
		return err
	}

	return nil
}
*/

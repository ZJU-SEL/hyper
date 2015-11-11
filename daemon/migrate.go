package daemon

import (
	"fmt"
	"github.com/hyperhq/hyper/engine"
	"github.com/hyperhq/runv/hypervisor"
	"github.com/hyperhq/runv/hypervisor/types"
	"github.com/hyperhq/runv/lib/glog"
)

func (daemon *Daemon) CmdVmMigrate(job *engine.Job) error {
	if len(job.Args) == 0 {
		return fmt.Errorf("Can not execute 'migrate' command without podname and targetIP")
	}
	podId := job.Args[0]
	targetIP := job.Args[1]
	daemon.PodList.Lock()
	glog.V(2).Infof("lock PodList")
	defer glog.V(2).Infof("unlock PodList")
	defer daemon.PodList.Unlock()
	code, cause, err := daemon.MigrateVm(podId, targetIP)
	if err != nil {
		return err
	}

	v := &engine.Env{}
	v.Set("ID", podId)
	v.SetInt("Code", code)
	v.Set("Cause", cause)
	if _, err := v.WriteTo(job.Stdout); err != nil {
		return err
	}

	return nil
}

func (daemon *Daemon) MigrateVm(podId, targetIP string) (int, string, error) {
	glog.V(1).Infof("now we are in the MigrateVm, it worked well ... for now")
	var Response *types.VmResponse
	Pod, ok := daemon.PodList.Get(podId)
	if !ok {
		glog.Errorf("Can not find pod(%s)", podId)
		return -1, "", fmt.Errorf("Can not find pod(%s)", podId)
	}
	vm := Pod.vm
	PodEvent, Status, err := daemon.GetVmChan(vm)
	if err != nil {
		return -1, err.Error(), err
	}
	migrateEvent := &hypervisor.MigrateVmCommand{
		IP:   targetIP,
		Port: "12345",
	}
	PodEvent <- migrateEvent
	for {
		Response = <-Status
		glog.V(1).Infof("MigrateVm Got response: %d: %s", Response.Code, Response.Cause)
		if Response.Code == types.E_MIGRATE_TIMEOUT {
			break
		}
		break
	}
	return Response.Code, Response.Cause, nil
}

func (daemon *Daemon) GetVmChan(vm *hypervisor.Vm) (chan hypervisor.VmEvent, chan *types.VmResponse, error) {
	RequestChan, err := vm.GetRequestChan()
	if err != nil {
		return nil, nil, err
	}
	defer vm.ReleaseRequestChan(RequestChan)

	ResponseChan, err := vm.GetResponseChan()
	if err != nil {
		return nil, nil, err
	}
	defer vm.ReleaseResponseChan(ResponseChan)

	return RequestChan, ResponseChan, nil
}

package daemon

import (
	"fmt"
	"hyper/engine"
	"hyper/hypervisor"
)

func (daemon *Daemon) checkPoint(job *engine.Job) error {
	if len(job.Args)==0 {
		return fmt.Errorf("Can not execute 'stop' command without any pod name")
	}

	podId:=job.Args[0]
	
	vmid,err:=daemon.GetPodVmByName(podId)
	
	if err!=nil {
		fmt.Println("Cannt find vm of",podId)
		return nil
	}
	
	qemuPodEvent,_,_,err:= daemon.GetQemuChan(vmid)
	checkPointEvent:=&hypervisor.CheckPoint{}
	qemuPodEvent.(chan hypervisor.VmEvent) <- checkPointEvent
	
	return nil
}

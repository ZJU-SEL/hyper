package daemon

import (
	"fmt"
	"hyper/engine"
	"hyper/hypervisor"
    "hyper/types"
    "hyper/lib/glog"
)

func (daemon *Daemon) checkPoint(job *engine.Job) error {
    var qemuResponse *types.QemuResponse
	if len(job.Args) == 0 {
		return fmt.Errorf("Can not execute 'stop' command without any pod name")
	}

    var (
	    podId = job.Args[0]
    )
	vmId,err:=daemon.GetPodVmByName(podId)
	if err!=nil {
		fmt.Println("Cannt find vm of",podId)
		return nil
	}
	qemuPodEvent, qemuStatus, _, err:= daemon.GetQemuChan(vmId)
    if err != nil {
        return err
    }
	checkPointEvent:=&hypervisor.CheckPoint{}
	qemuPodEvent.(chan hypervisor.VmEvent) <- checkPointEvent
    for{
        select {
        case qemuResponse = <-qemuStatus.(chan *types.QemuResponse):
            glog.V(1).Infof("Got response: %d: %s", qemuResponse.Code, qemuResponse.Cause)
            break
        }
    }
    close(qemuStatus.(chan *types.QemuResponse))

    v := &engine.Env{}
    v.Set("ID", vmId)
    v.SetInt("Code", qemuResponse.Code)
    v.Set("Cause", qemuResponse.Cause)
    if _, err := v.WriteTo(job.Stdout); err != nil{
        return err
    }
	return nil
}

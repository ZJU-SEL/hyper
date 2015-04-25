package daemon

import (
	"os"
	"fmt"

	"dvm/engine"
	"dvm/lib/sysinfo"
)

func (daemon *Daemon) CmdInfo(job *engine.Job) error {
	cli := daemon.dockerCli
	body, _, err := cli.SendCmdInfo("")
	out := engine.NewOutput()
	remoteInfo, err := out.AddEnv()
	if err != nil {
		return err
	}
	if _, err := out.Write(body); err != nil {
		return fmt.Errorf("Error while reading remote info!\n")
	}
	out.Close()

	v := &engine.Env{}
	v.SetJson("ID", daemon.ID)
	if remoteInfo.Exists("Containers") {
		v.SetInt("Containers", remoteInfo.GetInt("Containers"))
	}

	// Get system infomation
	meminfo, err := sysinfo.GetMemInfo()
	osinfo, err := sysinfo.GetOSInfo()
	v.SetInt64("MemTotal", int64(meminfo.MemTotal))
	v.SetInt64("Pods", daemon.GetPodNum())
	v.Set("Operating System", osinfo.PrettyName)
	if hostname, err := os.Hostname(); err == nil {
		v.SetJson("Name", hostname)
	}
	if _, err := v.WriteTo(job.Stdout); err != nil {
		return err
	}
	return nil
}

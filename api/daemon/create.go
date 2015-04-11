package daemon

import (
	"fmt"
	"dvm/engine"
)

func (daemon *Daemon) CmdCreate(job *engine.Job) error {
	imgName := job.Args[0]
	cli := daemon.dockerCli
	body, _, err := cli.SendCmdCreate(imgName)
	if err != nil {
		return err
	}
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
	containerId := remoteInfo.Get("Id")
	if containerId != "" {
		v.Set("ContainerID", containerId)
		fmt.Printf("The ContainerID is %s\n", containerId)
	} else {
		return fmt.Errorf("DVM ERROR: AN error encountered during creating container!\n")
	}

	if _, err := v.WriteTo(job.Stdout); err != nil {
		return err
	}

	return nil
}

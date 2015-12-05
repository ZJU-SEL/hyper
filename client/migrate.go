package client

import (
	"fmt"
	"github.com/hyperhq/hyper/engine"
	"github.com/hyperhq/runv/hypervisor/types"
	gflag "github.com/jessevdk/go-flags"
	"net/url"
	"os"
	"strings"
)

func (cli *HyperClient) HyperCmdMigrate(args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s ERROR: At least has one argumet!\n", os.Args[0])
	}
	var parser = gflag.NewParser(nil, gflag.Default|gflag.IgnoreUnknown)
	parser.Usage = "hyper migrate POD_ID HOST:PORT\n\nmigrate a pod, or wait a migration\nexample:\n\thyper migrate pod-abcdefghijklm localhost:8888"

	args, err := parser.Parse()
	if err != nil {
		if !strings.Contains(err.Error(), "Usage") {
			return err
		} else {
			return nil
		}
	}

	var (
		v       url.Values = url.Values{}
		podId   string     = ""
		desAddr string     = ""
	)

	if len(args) != 3 {
		return fmt.Errorf(parser.Usage)
	}
	podId = args[1]
	desAddr = args[2]

	fmt.Println("Start to migrate Pod( %s )...", podId)
	//migrate pod data to destination daemon
	v.Set("podId", podId)
	v.Set("desAddr", desAddr)
	body, _, err := readBody(cli.call("POST", "/pod/migrate?"+v.Encode(), nil, nil))
	if err != nil {
		return err
	}
	out := engine.NewOutput()
	remoteInfo, err := out.AddEnv()
	if err != nil {
		return err
	}
	if _, err := out.Write(body); err != nil {
		return fmt.Errorf("Error reading remote info: %s", err)
	}
	out.Close()
	errCode := remoteInfo.GetInt("Code")
	if errCode != types.E_OK {
		return fmt.Errorf("")
	}
	fmt.Println("Pod( %s ) data migrate successfully, wait vm migrate...", podId)

	//if migrate pod data success, migrate vm machine to destination host
	body, _, err = readBody(cli.call("POST", "/vm/migrate?"+v.Encode(), nil, nil))
	if err != nil {
		return err
	}
	out = engine.NewOutput()
	remoteInfo, err = out.AddEnv()
	if err != nil {
		return err
	}

	if _, err := out.Write(body); err != nil {
		return fmt.Errorf("Error reading remote info: %s", err)
	}
	out.Close()
	errCode = remoteInfo.GetInt("Code")
	if errCode != types.E_OK {
		return fmt.Errorf("")
	}
	return nil
}

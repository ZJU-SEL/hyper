package client

import (
	"fmt"
	"github.com/hyperhq/hyper/engine"
	"github.com/hyperhq/runv/hypervisor/types"
	gflag "github.com/jessevdk/go-flags"
	"net/url"
	"strings"
)

func (cli *HyperClient) HyperCmdMigrate(args ...string) error {
	var parser = gflag.NewParser(nil, gflag.Default)
	parser.Usage = "migrate POD_ID IP_ADDRESS\n\nmigrate a pod"
	args, err := parser.Parse()
	if err != nil {
		if !strings.Contains(err.Error(), "Usage") {
			return err
		} else {
			return nil
		}
	}
	if len(args) < 3 {
		return fmt.Errorf(parser.Usage)
	}
	podID := args[1]
	targetIP := args[2]
	v := url.Values{}
	v.Set("podId", podID)
	v.Set("targetIP", targetIP)
	body, _, err := readBody(cli.call("POST", "/vm/migrate?"+v.Encode(), nil, nil))
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
	return nil
}

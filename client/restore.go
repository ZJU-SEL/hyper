package client

import (
	"fmt"
	gflag "github.com/jessevdk/go-flags"
	"net/url"
	"strings"
	//    "github.com/hyperhq/hyper/engine"
	//    "github.com/hyperhq/runv/hypervisor/types"
)

func (cli *HyperClient) HyperCmdRestore(args ...string) error {
	var parser = gflag.NewParser(nil, gflag.Default)
	parser.Usage = "resotre POD_ID\n\nrestore a pod"
	args, err := parser.Parse()
	if err != nil {
		if !strings.Contains(err.Error(), "Usage") {
			return err
		} else {
			return nil
		}
	}
	if len(args) < 2 {
		return fmt.Errorf(parser.Usage)
	}
	podID := args[1]
	v := url.Values{}
	v.Set("podId", podID)
	cli.call("POST", "/vm/restore?"+v.Encode(), nil, nil)
	/*   body, _, err := readBody(cli.call("POST", "/vm/checkpoint?"+v.Encode(), nil, nil))
	if err != nil{
	    return err
	}
	out := engine.NewOutput()
	remoteInfo, err := out.AddEnv()
	if err != nil{
	    return err
	}

	if _, err := out.Write(body); err != nil{
	    return fmt.Errorf("Error reading remote info: %s", err)
	}
	out.Close()
	errCode := remoteInfo.GetInt("Code")
	if errCode != types.E_OK{
	    return fmt.Errorf("")
	}
	*/
	return nil
}

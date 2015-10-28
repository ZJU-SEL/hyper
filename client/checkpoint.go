package client

import (
	"fmt"
	"net/url"
    "strings"

	gflag "github.com/jessevdk/go-flags"
    "hyper/engine"
    "hyper/types"
)


func (cli *HyperClient) HyperCmdCheckpoint(args...string) error {
	var parser=gflag.NewParser(nil, gflag.Default)
    parser.Usage = "checkpoint POD_ID\n\ncheckpoint a pod"
	args,err:=parser.Parse()
	if err!=nil {
        if !strings.Contains(err.Error(), "Usage"){
            return err
        }else{
            return nil
        }
	}
    if len(args) < 2{
        return fmt.Errorf("\"checkpoint\" requires a minimum of 1 argument,please provide POD ID.\n")
    }
	podID:=args[1]
	fmt.Println("PodId:",podID)
    v:=url.Values{}
	v.Set("podId", podID)
    body, _, err := readBody(cli.call("GET","/checkpoint?"+v.Encode(),nil,nil))
    if err != nil{
        return err
    }
    out := engine.NewOutput()
    remoteInfo, err := out.AddEnv()
    if err != nil {
        return err
    }

    if _, err := out.Write(body); err != nil {
        return fmt.Errorf("Error reading remote info: %s, err")
    }
    out.Close()
    errCode := remoteInfo.GetInt("Code")
    if errCode == types.E_OK || errCode == types.E_VM_SHUTDOWN {
        //
    }else {
        return fmt.Errorf("Error code is %d, Cause is %s", remoteInfo.GetInt("Code"), remoteInfo.Get("Cause"))

    }
	return nil
}

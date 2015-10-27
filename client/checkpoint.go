package client

import (
	"fmt"
	"net/url"

	gflag "github.com/jessevdk/go-flags"
)


func (cli *HyperClient) HyperCmdCheckpoint(args...string) error {

	var opts struct {
	
	}
	var parser=gflag.NewParser(&opts,gflag.Default)
	args,err:=parser.Parse()
	
	if err!=nil {
		fmt.Println("parse wrong")
	}
	
	podID:=args[1]
	fmt.Println("PodId:",podID)
	
	v:=url.Values{}
	v.Set("podId",podID)
	
	cli.call("GET","/checkpoint?"+v.Encode(),nil,nil)
	
	return nil
}

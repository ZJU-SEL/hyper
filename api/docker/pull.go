package docker

import (
	"fmt"
	"net/url"
	"os"
)

func (cli *DockerCli) SendCmdPull(args ...string) ([]byte, int, error) {
	// We need to create a container via an image object.  If the image
	// is not stored locally, so we need to pull the image from the Docker HUB.

	// Get a Repository name and tag name from the argument, but be careful
	// with the Repository name with a port number.  For example:
	//      localdomain:5000/samba/hipache:latest
	image := args[0]
	repos, tag := parseTheGivenImageName(image)
	if tag == "" {
		tag = "latest"
	}

	// Pull the image from the docker HUB
	v := url.Values{}
	v.Set("fromImage", repos)
	v.Set("tag", tag)
	fmt.Printf("The Repository is %s, and the tag is %s\n", repos, tag)
	fmt.Printf("pull the image from the repository!\n")
	err := cli.Stream("POST", "/images/create?"+ v.Encode(), nil, os.Stdout, nil)
	if err != nil {
		return nil, -1, err
	}
	return nil, 200, nil
}

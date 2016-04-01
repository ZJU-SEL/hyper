// +build !exclude_graphdriver_rbd

package daemon

import (
	_ "github.com/hyperhq/hyper/lib/docker/daemon/graphdriver/rbd"
)

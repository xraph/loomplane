//go:build !linux

package node

import (
	"context"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func (p *hostProbes) cgroup() (nodeproto.CgroupLimits, error) {
	return nodeproto.CgroupLimits{}, ErrUnsupportedProbe
}

func (p *hostProbes) gpu(context.Context) (nodeproto.GPUProbe, error) {
	return nodeproto.GPUProbe{}, ErrUnsupportedProbe
}

func (p *hostProbes) filesystem(context.Context, string) (nodeproto.Filesystem, error) {
	return nodeproto.Filesystem{}, ErrUnsupportedProbe
}

func (p *hostProbes) listeners() (nodeproto.Listeners, error) {
	return nodeproto.Listeners{}, ErrUnsupportedProbe
}

//go:build !linux

package node

import (
	"context"
	"errors"
	"testing"
)

func TestProbesUnsupportedOffLinux(t *testing.T) {
	p := newHostProbes(func(context.Context) string { return "" })
	if _, err := p.cgroup(); !errors.Is(err, ErrUnsupportedProbe) {
		t.Fatalf("cgroup: %v", err)
	}
	if _, err := p.gpu(context.Background()); !errors.Is(err, ErrUnsupportedProbe) {
		t.Fatalf("gpu: %v", err)
	}
	if _, err := p.filesystem(context.Background(), "/var/lib/docker"); !errors.Is(err, ErrUnsupportedProbe) {
		t.Fatalf("filesystem: %v", err)
	}
	if _, err := p.listeners(); !errors.Is(err, ErrUnsupportedProbe) {
		t.Fatalf("listeners: %v", err)
	}
}

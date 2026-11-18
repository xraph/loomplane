package cloudinfra

import (
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

// Each catalog mismatch names its cause, so the review says what to change
// rather than "invalid infrastructure request".
func TestValidateOptionsNamesTheCause(t *testing.T) {
	options := provider.ServerOptions{
		Regions: []provider.ServerRegion{{ID: "nyc3"}, {ID: "ric1"}},
		Sizes: []provider.ServerSize{
			{ID: "s-1vcpu-1gb", CPUs: 1, MemoryMiB: 1024, DiskGB: 25, Regions: []string{"nyc3", "ric1"}},
			{ID: "s-4vcpu-8gb-intel", CPUs: 4, MemoryMiB: 8192, DiskGB: 160, Regions: []string{"nyc3"}},
		},
		Images: []provider.ServerImage{
			{ID: "ubuntu-24-04-x64", Name: "Ubuntu 24.04", Bootstrap: true, Regions: []string{"nyc3", "ric1"}},
			{ID: "ubuntu-big", Name: "Ubuntu big", Bootstrap: true, MinDiskGB: 50, Regions: []string{"nyc3", "ric1"}},
			{ID: "ubuntu-nyc", Name: "Ubuntu NYC", Bootstrap: true, Regions: []string{"nyc3"}},
			{ID: "debian-12-x64", Name: "Debian 12", Regions: []string{"nyc3", "ric1"}},
		},
	}
	for _, tc := range []struct {
		name string
		in   ServerInput
		want string
	}{
		{"region", ServerInput{Region: "sfo9", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64"}, `Region "sfo9" isn't available`},
		{"size", ServerInput{Region: "nyc3", Size: "s-99", Image: "ubuntu-24-04-x64"}, `Size "s-99" isn't offered`},
		{"size in region", ServerInput{Region: "ric1", Size: "s-4vcpu-8gb-intel", Image: "ubuntu-24-04-x64"}, "Size s-4vcpu-8gb-intel isn't offered in ric1"},
		{"image unknown", ServerInput{Region: "nyc3", Size: "s-1vcpu-1gb", Image: "nope"}, `Image "nope" can't run`},
		{"image without bootstrap", ServerInput{Region: "nyc3", Size: "s-1vcpu-1gb", Image: "debian-12-x64"}, `Image "debian-12-x64" can't run`},
		{"image in region", ServerInput{Region: "ric1", Size: "s-1vcpu-1gb", Image: "ubuntu-nyc"}, "Image Ubuntu NYC isn't available in ric1"},
		{"image disk", ServerInput{Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-big"}, "needs at least 50 GB of disk, and size s-1vcpu-1gb has 25 GB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOptions(options, tc.in)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
	if err := validateOptions(options, ServerInput{Region: "nyc3", Size: "s-4vcpu-8gb-intel", Image: "ubuntu-big"}); err != nil {
		t.Fatalf("valid choice refused: %v", err)
	}
}

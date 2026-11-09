package planning

import (
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

func TestManagedSchedulingArchitecture(t *testing.T) {
	now := time.Now()
	base := cloudinfra.ClusterCapacitySnapshot{CheckedAt: now, Complete: true, WorkloadsComplete: true, Nodes: []cloudinfra.ClusterNodeCapacity{{Ready: true, Architecture: "arm64"}}, SchedulingProfiles: []cloudinfra.ClusterSchedulingProfile{{Architecture: "arm64", Digest: "current-profile", TemplateDigest: "template"}}}
	for _, name := range []string{"homogeneous", "mixed nodes", "mixed profiles", "stale", "cordoned", "empty profile", "image mismatch", "builder mismatch"} {
		t.Run(name, func(t *testing.T) {
			in := base
			cfg := &ServiceConfig{}
			switch name {
			case "mixed nodes":
				in.Nodes = append(append([]cloudinfra.ClusterNodeCapacity(nil), in.Nodes...), cloudinfra.ClusterNodeCapacity{Ready: true, Architecture: "amd64"})
			case "mixed profiles":
				in.SchedulingProfiles = append(append([]cloudinfra.ClusterSchedulingProfile(nil), in.SchedulingProfiles...), cloudinfra.ClusterSchedulingProfile{Architecture: "amd64", Digest: "other", TemplateDigest: "other"})
			case "stale":
				in.CheckedAt = now.Add(-10 * time.Minute)
			case "cordoned":
				in.Nodes = []cloudinfra.ClusterNodeCapacity{{Ready: true, Unschedulable: true, Architecture: "arm64"}}
			case "empty profile":
				in.SchedulingProfiles = nil
			case "image mismatch":
				cfg.Artifact = &ReusedArtifact{Architecture: "amd64"}
			case "builder mismatch":
				cfg.BuildTarget = &BuildTarget{BuilderSettings: map[string]string{"architecture": "amd64"}}
			}
			arch, err := qualifiedProfileArchitecture(in, cfg)
			if name == "homogeneous" {
				if err != nil || arch != "arm64" {
					t.Fatal(arch, err)
				}
			} else if err == nil {
				t.Fatal("unqualified architecture", arch)
			}
		})
	}
}

func TestManagedServiceFinalizers(t *testing.T) {
	for _, tt := range []struct {
		cfg     ServiceConfig
		blocked bool
	}{
		{ServiceConfig{DatabaseGateway: &DatabaseGatewayConfig{Name: "gateway"}}, true},
		{ServiceConfig{Env: []EnvLink{{Subject: ResourceSubject, Field: "consumer:url"}}}, true},
		{ServiceConfig{Env: []EnvLink{{Subject: ResourceSubject, Field: "address"}}}, false},
	} {
		if err := tt.cfg.CheckManagedServiceFinalizers(); (err != nil) != tt.blocked {
			t.Fatal(tt.cfg, err)
		}
	}
}

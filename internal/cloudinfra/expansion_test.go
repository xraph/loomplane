package cloudinfra

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

func TestExpansionRejectsUntrustedDemand(t *testing.T) {
	s := NewService(Deps{})
	_, err := s.EnsureCapacity(t.Context(), CapacityDemand{OrgID: "org", PlanID: "plan", Service: "app"})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("unwired authority: %v", err)
	}
}

func TestExpansionAttachments(t *testing.T) {
	host := ResourceVector{Servers: 1, CPUMillis: 2000, MemoryBytes: 2 << 30, StorageBytes: 20_000_000_000}
	reserve := ResourceVector{CPUMillis: 500, MemoryBytes: 512 << 20}
	used := ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30}
	if !expansionDemandFits(host, reserve, used, ResourceVector{CPUMillis: 500, MemoryBytes: 512 << 20}) {
		t.Fatal("exact headroom rejected")
	}
	if expansionDemandFits(host, reserve, used, ResourceVector{CPUMillis: 501, MemoryBytes: 512 << 20}) {
		t.Fatal("future host overbooked")
	}
	if expansionDemandFits(host, reserve, used, ResourceVector{CPUMillis: -1}) {
		t.Fatal("negative demand accepted")
	}
}

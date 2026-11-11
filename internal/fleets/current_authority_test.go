package fleets

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetLockedAuthorityUsesSuppliedInstant(t *testing.T) {
	at := time.Date(2001, 1, 1, 0, 0, 0, 123456789, time.UTC)
	until := at.Add(time.Nanosecond)
	p := security.Principal{KeyID: "credential"}
	request := OperationRequest{GrantID: "grant", Kind: "create", StageID: "stage", ReleaseID: "release", ProfileID: "profile", CloudID: "cloud", Region: "region", ExternalRef: "external", IdempotencyKey: "request", SnapshotDigest: "sha256:" + strings.Repeat("a", 64), Demand: OperationDemand{CPUMillis: 100, MemoryMiB: 128, StorageGiB: 1, HourlyCostMicros: 1, Currency: "USD", PriceKnown: true}}
	grant := fleet.Grant{Status: "active", Input: fleet.CreateGrantInput{CredentialID: p.KeyID, StageID: request.StageID, ReleaseIDs: []string{request.ReleaseID}, ProfileIDs: []string{request.ProfileID}, CloudIDs: []string{request.CloudID}, Regions: []string{request.Region}, Actions: []string{request.Kind}, ExpiresAt: until, Limits: fleet.GrantLimits{MaxInstances: 1, CPUMillis: 100, MemoryMiB: 128, StorageGiB: 1, HourlyCostMicros: 1, Currency: "USD"}}}
	proof := LockedInstanceAuthority{authority: OperationAuthority{Principal: p, Request: request, ExpiresAt: until}, grant: grant, authorityExpiry: until, grantExpiry: until}
	if err := proof.ValidateAt(at); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*LockedInstanceAuthority){
		"authority snapshot": func(p *LockedInstanceAuthority) { p.authority.ExpiresAt = at },
		"authority column":   func(p *LockedInstanceAuthority) { p.authorityExpiry = at },
		"grant snapshot":     func(p *LockedInstanceAuthority) { p.grant.Input.ExpiresAt = at },
		"grant column":       func(p *LockedInstanceAuthority) { p.grantExpiry = at },
		"revoked":            func(p *LockedInstanceAuthority) { p.grant.Status = "revoked" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			change(&candidate)
			if err := candidate.ValidateAt(at); !errors.Is(err, ErrAuthorityRevoked) {
				t.Fatalf("accepted expired fact: %v", err)
			}
		})
	}
	if err := proof.ValidateAt(time.Time{}); !errors.Is(err, ErrAuthorityRevoked) {
		t.Fatal(err)
	}
}

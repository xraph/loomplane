package connections

import (
	"errors"
	"maps"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

func TestManagedAccountIdentityRequiresVerifiedScope(t *testing.T) {
	for _, tc := range []struct {
		name, cloud, account string
		details              map[string]string
	}{
		{name: "GCP without project", cloud: GCP, account: "display-only"},
		{name: "GCP blank project", cloud: GCP, account: "display-only", details: map[string]string{"project_id": " "}},
		{name: "Render without owner", cloud: Render, account: "workspace-name"},
		{name: "Render blank owner", cloud: Render, account: "workspace-name", details: map[string]string{"owner_id": " "}},
		{name: "AWS without account", cloud: AWS, details: map[string]string{"region": "us-east-1"}},
		{name: "AWS blank account", cloud: AWS, account: " ", details: map[string]string{"region": "us-east-1"}},
		{name: "AWS without region", cloud: AWS, account: "123456789012"},
		{name: "AWS blank region", cloud: AWS, account: "123456789012", details: map[string]string{"region": " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AccountIdentity(tc.cloud, tc.account, tc.details); got != "" {
				t.Fatalf("missing verified scope produced identity %s", got)
			}
		})
	}
}

func TestManagedIdentityProtectionMissingScopeAndRenewal(t *testing.T) {
	for _, tc := range []struct{ cloud, account, key, value string }{
		{GCP, "project-a", "project_id", "project-a"},
		{Render, "Workspace", "owner_id", "tea-a"},
		{AWS, "123456789012", "region", "us-east-1"},
	} {
		for _, scenario := range []string{"missing old", "missing candidate", "both missing", "same account"} {
			for _, operation := range []string{"replace", "recheck"} {
				t.Run(tc.cloud+"/"+scenario+"/"+operation, func(t *testing.T) {
					oldAccount, nextAccount := tc.account, tc.account
					old, next := map[string]string{tc.key: tc.value}, map[string]string{tc.key: tc.value, "permissions": "refreshed"}
					clearOld := scenario == "missing old" || scenario == "both missing"
					clearNext := scenario == "missing candidate" || scenario == "both missing"
					if clearOld {
						if tc.cloud == AWS {
							oldAccount = ""
						} else {
							delete(old, tc.key)
						}
					}
					if clearNext {
						if tc.cloud == AWS {
							nextAccount = ""
						} else {
							delete(next, tc.key)
						}
					}
					store := &identityStore{row: Connection{ID: "conn_1", OrgID: "org_1", Cloud: tc.cloud, Account: oldAccount, Details: old, Status: StatusReady}}
					vault := &identitySecrets{value: []byte("old")}
					svc := NewService(store, vault, map[string]Checker{tc.cloud: identityChecker{Check{Account: nextAccount, Details: next}}}, identityAuthz{}, nil, nil)
					principal := security.Principal{OrgID: "org_1"}
					var err error
					if operation == "replace" {
						_, err = svc.Replace(t.Context(), principal, "conn_1", ReplaceInput{Credential: "renewed"})
					} else {
						_, err = svc.Recheck(t.Context(), principal, "conn_1")
					}
					if scenario == "same account" {
						if err != nil || store.writes != 1 {
							t.Fatalf("valid renewal failed: %v writes=%d", err, store.writes)
						}
						if operation == "replace" && (vault.puts != 1 || string(vault.value) != "renewed") {
							t.Fatal("renewed credential not saved")
						}
					} else {
						if !errors.Is(err, ErrInvalid) {
							t.Fatalf("missing identity accepted: %v", err)
						}
						if store.writes != 0 || vault.puts != 0 || string(vault.value) != "old" || store.row.Account != oldAccount || !maps.Equal(store.row.Details, old) {
							t.Fatal("missing identity rejection mutated stored state")
						}
					}
				})
			}
		}
	}
}

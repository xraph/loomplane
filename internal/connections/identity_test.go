package connections

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

type identityStore struct {
	Store
	row    Connection
	writes int
}

func (s *identityStore) GetConnection(_ context.Context, org, id string) (*Connection, error) {
	if org != s.row.OrgID || id != s.row.ID {
		return nil, ErrNotFound
	}
	c := s.row
	c.Details = maps.Clone(c.Details)
	return &c, nil
}
func (s *identityStore) UpdateConnection(_ context.Context, c *Connection) error {
	s.row = *c
	s.writes++
	return nil
}

type identitySecrets struct {
	value []byte
	puts  int
}

func (s *identitySecrets) Put(_ context.Context, _, _ string, v []byte) error {
	s.value = append([]byte(nil), v...)
	s.puts++
	return nil
}
func (s *identitySecrets) Reveal(context.Context, string, string) ([]byte, error) {
	return s.value, nil
}
func (*identitySecrets) Delete(context.Context, string, string) error { return nil }

type identityAuthz struct{}

func (identityAuthz) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type identityChecker struct{ result Check }

func (c identityChecker) Parse([]byte, map[string]string) (Check, error) { return c.result, nil }
func (c identityChecker) Check(context.Context, []byte, map[string]string) (Check, error) {
	return c.result, nil
}

func TestIdentityProtection(t *testing.T) {
	for _, tc := range []struct {
		name, cloud, account, nextAccount string
		details, nextDetails              map[string]string
	}{
		{name: "different account", cloud: GCP, account: "project-a", nextAccount: "project-b", details: map[string]string{"project_id": "project-a"}, nextDetails: map[string]string{"project_id": "project-b"}},
		{name: "Vercel team", cloud: Vercel, account: "team", nextAccount: "team", details: map[string]string{"team_id": "team_a"}, nextDetails: map[string]string{"team_id": "team_b"}},
		{name: "Kubernetes namespace", cloud: Kubernetes, account: "context/default", nextAccount: "context/default", details: map[string]string{"endpoint": "https://cluster", "namespace": "a", "cluster_ca": "ca"}, nextDetails: map[string]string{"endpoint": "https://cluster", "namespace": "b", "cluster_ca": "ca"}},
	} {
		for _, operation := range []string{"replace", "recheck"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				st := &identityStore{row: Connection{ID: "conn_1", OrgID: "org_1", Cloud: tc.cloud, Account: tc.account, Details: tc.details, Status: StatusReady}}
				vault := &identitySecrets{value: []byte("old")}
				svc := NewService(st, vault, map[string]Checker{tc.cloud: identityChecker{Check{Account: tc.nextAccount, Details: tc.nextDetails}}}, identityAuthz{}, nil, nil)
				p := security.Principal{OrgID: "org_1"}
				var err error
				if operation == "replace" {
					_, err = svc.Replace(t.Context(), p, "conn_1", ReplaceInput{Credential: "new"})
				} else {
					_, err = svc.Recheck(t.Context(), p, "conn_1")
				}
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("changed identity accepted: %v", err)
				}
				if vault.puts != 0 || string(vault.value) != "old" || st.writes != 0 {
					t.Fatalf("identity rejection mutated state: vault=%+v writes=%d", vault, st.writes)
				}
			})
		}
	}
}

func TestIdentityProtection_sameAccountRotation(t *testing.T) {
	st := &identityStore{row: Connection{ID: "conn_1", OrgID: "org_1", Cloud: GCP, Account: "project-a", Details: map[string]string{"project_id": "project-a", "service_account": "old@project"}}}
	vault := &identitySecrets{value: []byte("old")}
	svc := NewService(st, vault, map[string]Checker{GCP: identityChecker{Check{Account: "project-a", Details: map[string]string{"project_id": "project-a", "service_account": "new@project", "permissions": "checked"}}}}, identityAuthz{}, nil, nil)
	if _, err := svc.Replace(t.Context(), security.Principal{OrgID: "org_1"}, "conn_1", ReplaceInput{Credential: "rotated"}); err != nil {
		t.Fatal(err)
	}
	if vault.puts != 1 || string(vault.value) != "rotated" || st.writes != 1 {
		t.Fatalf("rotation not saved: %+v %+v", vault, st)
	}
}

func TestAccountIdentity_stableScope(t *testing.T) {
	for _, tc := range []struct {
		cloud, account, key string
		details             map[string]string
	}{
		{GCP, "project", "project_id", map[string]string{"project_id": "project"}},
		{Vercel, "team", "team_id", map[string]string{"team_id": "team_a"}},
		{Render, "Workspace", "owner_id", map[string]string{"owner_id": "tea-a"}},
		{AWS, "123456789012", "region", map[string]string{"region": "us-east-1"}},
		{Kubernetes, "ctx/default", "namespace", map[string]string{"endpoint": "https://cluster", "namespace": "default", "cluster_ca": "ca"}},
		{Server, "deploy@host:22", "docker_socket", map[string]string{"host": "host", "port": "22", "user": "deploy", "docker_socket": "/var/run/docker.sock", "host_key": "key"}},
		{Registry, "registry/repo", "repository", map[string]string{"endpoint": "https://registry", "repository": "repo"}},
	} {
		t.Run(tc.cloud, func(t *testing.T) {
			original := AccountIdentity(tc.cloud, tc.account, tc.details)
			details := maps.Clone(tc.details)
			details["permissions"] = "checked"
			details["checked_at"] = "later"
			details["token"] = "credential"
			details["service_account"] = "rotated"
			details["principal"] = "new role"
			if changed := AccountIdentity(tc.cloud, tc.account, details); changed != original {
				t.Fatal("credential or permission observation changed identity")
			}
			details[tc.key] = "different"
			if changed := AccountIdentity(tc.cloud, tc.account, details); changed == original {
				t.Fatalf("scope change %s was lost", tc.key)
			}
		})
	}
	if AccountIdentity(AWS, "account-a", nil) == AccountIdentity(AWS, "account-b", nil) {
		t.Fatal("AWS accounts collapsed")
	}
	if AccountIdentity(Vercel, "alice", nil) == AccountIdentity(Vercel, "bob", nil) {
		t.Fatal("personal Vercel accounts collapsed")
	}
}

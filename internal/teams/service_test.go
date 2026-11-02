package teams

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

// countingAuthz fails the test's expectations by being called at all.
type countingAuthz struct{ calls int }

func (a *countingAuthz) Require(context.Context, security.Principal, security.Permission, string) error {
	a.calls++
	return nil
}

func (a *countingAuthz) Sync(context.Context, string, string) error { a.calls++; return nil }

func (a *countingAuthz) TrySync(context.Context, string, string) (bool, error) {
	a.calls++
	return true, nil
}

// Loomplane automation and the agent never change a team. Every write
// refuses them before it checks a permission, looks anyone up or opens a
// transaction (the service has no database here, so any of those would
// panic).
func TestWritesRefuseAutomation(t *testing.T) {
	authz := &countingAuthz{}
	lookups := 0
	s := NewService(Deps{Authz: authz, Members: func(context.Context, string, string) (string, bool, error) {
		lookups++
		return "owner", true, nil
	}})
	ctx := context.Background()
	name := "Payments"
	for who, p := range map[string]security.Principal{
		"automation": security.SystemPrincipal("aorg_1"),
		"agent":      {UserID: "agent:aorg_1", OrgID: "aorg_1"},
	} {
		writes := map[string]func() error{
			"Create": func() error { _, err := s.Create(ctx, p, CreateTeamInput{Name: name}); return err },
			"Update": func() error { _, err := s.Update(ctx, p, "team_1", UpdateTeamInput{Name: &name}); return err },
			"Delete": func() error { return s.Delete(ctx, p, "team_1") },
			"AddMember": func() error {
				_, err := s.AddMember(ctx, p, "team_1", "ausr_grace")
				return err
			},
			"RemoveMember": func() error {
				_, err := s.RemoveMember(ctx, p, "team_1", "ausr_grace")
				return err
			},
			"SetGrant": func() error {
				_, err := s.SetGrant(ctx, p, "team_1", SetGrantInput{Role: "viewer"})
				return err
			},
			"RemoveGrant": func() error {
				_, err := s.RemoveGrant(ctx, p, "team_1", "tgrant_1")
				return err
			},
		}
		for op, write := range writes {
			if err := write(); !errors.Is(err, security.ErrForbidden) {
				t.Errorf("%s by %s: %v, want ErrForbidden", op, who, err)
			}
		}
	}
	if authz.calls != 0 || lookups != 0 {
		t.Fatalf("%d authorizer calls and %d lookups, want none", authz.calls, lookups)
	}
}

package githubtest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/repositories/githubtest"
)

func TestFakeRefusesIssuesTokensWithoutThePermission(t *testing.T) {
	ctx := context.Background()
	gh := githubtest.New(t)
	gh.AddInstallation(7, "acme", map[string]string{"contents": "read", "metadata": "read"})
	app, err := repositories.NewApp(gh.AppConfig())
	if err != nil {
		t.Fatal(err)
	}

	if tok, err := app.RepositoryToken(ctx, 7, "api"); err != nil || tok == "" {
		t.Fatalf("a contents token: %q, %v", tok, err)
	}
	if _, err := app.IssuesToken(ctx, 7, "api"); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("an issues token before acceptance: %v", err)
	}

	gh.SetPermissions(7, map[string]string{"contents": "read", "issues": "write", "metadata": "read"})
	if tok, err := app.IssuesToken(ctx, 7, "api"); err != nil || tok == "" {
		t.Fatalf("an issues token after acceptance: %q, %v", tok, err)
	}
	if _, err := app.IssuesToken(ctx, 8, "api"); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("an unknown installation: %v", err)
	}
}

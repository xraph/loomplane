package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
)

// ErrBadSignature is a webhook delivery GitHub didn't sign with the
// secret Loomplane has.
var ErrBadSignature = errors.New("the webhook's signature doesn't match")

// Webhooks re-analyzes projects when GitHub says their repository's
// default branch moved, so the next plan has the new commit without
// anyone pressing Analyze.
type Webhooks struct {
	// Secret is the webhook secret set on the GitHub App.
	Secret []byte
	// Installations lists the organizations that connected an App
	// installation.
	Installations func(ctx context.Context, installationID int64) ([]string, error)
	// Projects lists projects in any organization whose repository URL
	// mentions the repository; Handle checks each one exactly.
	Projects func(ctx context.Context, fullName string) ([]*projects.Project, error)
	// Start queues an analysis of a project's default branch.
	Start func(ctx context.Context, orgID, projectID string) (*Analysis, error)
}

// pushEvent is the part of GitHub's push payload Handle reads.
type pushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
	} `json:"repository"`
	Installation *struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// Verify checks a delivery's X-Hub-Signature-256 header against its body.
func (w *Webhooks) Verify(signature string, body []byte) error {
	got, ok := strings.CutPrefix(signature, "sha256=")
	if !ok || len(w.Secret) == 0 {
		return ErrBadSignature
	}
	want := hmac.New(sha256.New, w.Secret)
	want.Write(body)
	sum, err := hex.DecodeString(got)
	if err != nil || !hmac.Equal(sum, want.Sum(nil)) {
		return ErrBadSignature
	}
	return nil
}

// Handle acts on a verified delivery and returns how many analyses it
// started. Only pushes to a repository's default branch do anything.
func (w *Webhooks) Handle(ctx context.Context, event string, body []byte) (int, error) {
	if event != "push" {
		return 0, nil
	}
	var push pushEvent
	if err := json.Unmarshal(body, &push); err != nil {
		return 0, fmt.Errorf("read the push: %w", err)
	}
	repo := push.Repository
	if push.Deleted || repo.FullName == "" || push.Ref != "refs/heads/"+repo.DefaultBranch {
		return 0, nil
	}
	// Which organizations may hear about it: those that connected the
	// installation the push came through, or, for a public repository's
	// own webhook, anyone with a project on it.
	var orgs map[string]bool
	if push.Installation != nil {
		ids, err := w.Installations(ctx, push.Installation.ID)
		if err != nil {
			return 0, err
		}
		orgs = map[string]bool{}
		for _, id := range ids {
			orgs[id] = true
		}
	} else if repo.Private {
		return 0, nil
	}
	candidates, err := w.Projects(ctx, repo.FullName)
	if err != nil {
		return 0, err
	}
	started := 0
	var errs []error
	for _, p := range candidates {
		if orgs != nil && !orgs[p.OrgID] {
			continue
		}
		r, err := repositories.ParseGitHubURL(p.RepositoryURL)
		if err != nil || !strings.EqualFold(r.FullName(), repo.FullName) {
			continue
		}
		switch _, err := w.Start(ctx, p.OrgID, p.ID); {
		case err == nil:
			started++
		case errors.Is(err, ErrAlreadyActive):
		default:
			errs = append(errs, fmt.Errorf("analyze %s: %w", p.ID, err))
		}
	}
	return started, errors.Join(errs...)
}

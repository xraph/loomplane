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
)

// ErrBadSignature is a webhook delivery that wasn't signed with the secret
// Loomplane has for it.
var ErrBadSignature = errors.New("the webhook's signature doesn't match")

// Webhooks re-analyzes the sources tracking a repository's default branch
// when GitHub says it moved, so the next plan has the new commit without
// anyone pressing Analyze.
type Webhooks struct {
	// Secret is the webhook secret set on the GitHub App.
	Secret []byte
	// Installations lists the organizations that connected an App
	// installation.
	Installations func(ctx context.Context, installationID int64) ([]string, error)
	// Sources lists the live sources in any organization that build from
	// the repository ("owner/name"), whatever its case.
	Sources func(ctx context.Context, fullName string) ([]*Source, error)
	// Start queues an analysis of a source's tracked branch.
	Start func(ctx context.Context, src *Source) (*Analysis, error)
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
	candidates, err := w.Sources(ctx, repo.FullName)
	if err != nil {
		return 0, err
	}
	allow := func(string) bool { return true }
	if orgs != nil {
		allow = func(orgID string) bool { return orgs[orgID] }
	}
	return startTracking(ctx, candidates, repo.FullName, repo.DefaultBranch, allow, w.Start)
}

// startTracking starts an analysis of each candidate that builds from the
// repository stored as key, whatever its case, and tracks defaultBranch,
// among the organizations allow lets through. It returns how many it
// started. A source with an analysis under way is left alone, and doesn't
// count. The other failures come back together, after every candidate has
// had its turn.
func startTracking(ctx context.Context, candidates []*Source, key, defaultBranch string, allow func(orgID string) bool, start func(context.Context, *Source) (*Analysis, error)) (int, error) {
	started := 0
	var errs []error
	for _, src := range candidates {
		if !allow(src.OrgID) {
			continue
		}
		// A source tracks the default branch when it names none. One that
		// tracks another branch didn't move.
		// A ref saved before sources were tidied may still read
		// "refs/heads/main".
		ref := branchRef(src.Ref)
		if !strings.EqualFold(src.Repository, key) || (ref != "" && ref != defaultBranch) {
			continue
		}
		switch _, err := start(ctx, src); {
		case err == nil:
			started++
		case errors.Is(err, ErrAlreadyActive):
		default:
			errs = append(errs, fmt.Errorf("analyze %s of %s: %w", src.Alias, src.ProjectID, err))
		}
	}
	return started, errors.Join(errs...)
}

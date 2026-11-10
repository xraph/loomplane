package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// gitlabTolerance is how far a signed delivery's timestamp may be from now,
// in either direction, before it counts as a replay.
const gitlabTolerance = 5 * time.Minute

// VerifyGitLab checks a delivery against the webhook secret of its
// connection. GitLab 19 and newer sign it the Standard Webhooks way: an
// HMAC-SHA256, keyed with the secret's bytes, of
// webhook-id.webhook-timestamp.body, sent as space-separated "v1,<base64>"
// entries in webhook-signature, any of which may match. Older versions send
// the secret, base64url without padding, in X-Gitlab-Token. A delivery with
// a signature is judged by it alone. Anything else is ErrBadSignature.
func VerifyGitLab(h http.Header, body, secret []byte, now time.Time) error {
	if len(secret) == 0 {
		return ErrBadSignature
	}
	if sigs := h.Get("Webhook-Signature"); sigs != "" {
		return verifyGitLabSignature(h, sigs, body, secret, now)
	}
	token := h.Get("X-Gitlab-Token")
	want := base64.RawURLEncoding.EncodeToString(secret)
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		return ErrBadSignature
	}
	return nil
}

func verifyGitLabSignature(h http.Header, sigs string, body, secret []byte, now time.Time) error {
	id, stamp := h.Get("Webhook-Id"), h.Get("Webhook-Timestamp")
	secs, err := strconv.ParseInt(stamp, 10, 64)
	if id == "" || err != nil {
		return ErrBadSignature
	}
	if age := now.Sub(time.Unix(secs, 0)); age > gitlabTolerance || age < -gitlabTolerance {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(id + "." + stamp + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	matched := false
	for _, entry := range strings.Fields(sigs) {
		enc, ok := strings.CutPrefix(entry, "v1,")
		if !ok {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			continue
		}
		// Every entry is compared, so the time taken doesn't say which
		// one matched.
		if hmac.Equal(got, want) {
			matched = true
		}
	}
	if !matched {
		return ErrBadSignature
	}
	return nil
}

// GitLabWebhooks re-analyzes the sources tracking a GitLab project's
// default branch when GitLab says it moved. Each delivery belongs to one
// connection, so it reaches that connection's organization and no other.
type GitLabWebhooks struct {
	// Target finds the organization, GitLab host and webhook secret of the
	// connection a delivery was addressed to. A connection that is gone, or
	// has no secret, is repositories.ErrGitLabConnectionNotFound.
	Target func(ctx context.Context, id string) (orgID, host string, secret []byte, err error)
	// Sources lists the live sources in any organization that build from
	// the repository ("host/namespace/project"), whatever its case.
	Sources func(ctx context.Context, key string) ([]*Source, error)
	// Start queues an analysis of a source's tracked branch.
	Start func(ctx context.Context, src *Source) (*Analysis, error)
	// Now is the time deliveries are judged against. Nil is time.Now.
	Now func() time.Time
	// Unreadable, when set, hears of a push its secret vouched for that
	// couldn't be read: not JSON, or with no project path or default
	// branch. It gets the connection the delivery came for and nothing
	// from the delivery itself, so a log line can say where to look
	// without repeating what GitLab sent.
	Unreadable func(ctx context.Context, id string)
}

// gitlabPush is the part of GitLab's Push Hook Handle reads.
type gitlabPush struct {
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
	} `json:"project"`
}

// Handle verifies a delivery for connection id and acts on it, returning
// how many analyses it started. The verdict on an unknown connection is
// repositories.ErrGitLabConnectionNotFound, and on a delivery its secret
// doesn't vouch for, ErrBadSignature. A verified body that isn't a readable
// push starts nothing and isn't an error. Only a push to a project's default
// branch does anything, and only among the connection's own organization's
// sources.
func (w *GitLabWebhooks) Handle(ctx context.Context, id string, h http.Header, body []byte) (int, error) {
	orgID, host, secret, err := w.Target(ctx, id)
	if err != nil {
		return 0, err
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	if verr := VerifyGitLab(h, body, secret, now()); verr != nil {
		return 0, verr
	}
	if h.Get("X-Gitlab-Event") != "Push Hook" {
		return 0, nil
	}
	// A delivery its secret vouches for but that isn't a push we can read
	// is taken and ignored: an error would make GitLab retry it, and
	// disable the hook after enough of them.
	var push gitlabPush
	if err = json.Unmarshal(body, &push); err != nil || push.Project.PathWithNamespace == "" || push.Project.DefaultBranch == "" {
		if w.Unreadable != nil {
			w.Unreadable(ctx, id)
		}
		return 0, nil
	}
	project := push.Project
	if orgID == "" || host == "" || isNullSHA(push.After) || push.Ref != "refs/heads/"+project.DefaultBranch {
		return 0, nil
	}
	key := host + "/" + project.PathWithNamespace
	candidates, err := w.Sources(ctx, key)
	if err != nil {
		return 0, err
	}
	// Only the connection's own organization: another's source on the same
	// project is none of this connection's business.
	allow := func(org string) bool { return org == orgID }
	return startTracking(ctx, candidates, key, project.DefaultBranch, allow, w.Start)
}

// isNullSHA reports whether a push's after is the all-zero commit GitLab
// sends when a branch was deleted.
func isNullSHA(after string) bool {
	return after != "" && strings.Trim(after, "0") == ""
}

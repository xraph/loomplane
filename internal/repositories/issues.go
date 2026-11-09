package repositories

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// IssuesPermission is the GitHub App permission Loomplane's agent
	// opens report issues with.
	IssuesPermission = "issues"
	// PermissionsTTL is how long an installation's permissions are
	// trusted before GitHub is asked again.
	PermissionsTTL = 5 * time.Minute
)

// Errors opening issues.
var (
	ErrIssuesNotAccepted = errors.New("the GitHub App installation hasn't accepted the Issues permission yet")
	ErrNoInstallation    = errors.New("no connected GitHub App installation can reach that repository")
)

// IssueAccess says whether Loomplane can open issues on a repository for
// a workspace, and why not when it can't.
type IssueAccess struct {
	Repository     string `json:"repository"`
	InstallationID int64  `json:"installation_id,omitempty"`
	Account        string `json:"account,omitempty"`
	Accepted       bool   `json:"accepted"`
	// Reason is for people, when not Accepted.
	Reason    string `json:"reason,omitempty"`
	ManageURL string `json:"manage_url,omitempty"`
}

// InstallationIssues is whether one connected installation has accepted
// the Issues permission. CheckedAt is when Loomplane last asked GitHub,
// which may have failed; Accepted is from the last read that didn't.
type InstallationIssues struct {
	Account     string     `json:"account"`
	AccountType string     `json:"account_type"`
	Accepted    bool       `json:"accepted"`
	ManageURL   string     `json:"manage_url"`
	CheckedAt   *time.Time `json:"checked_at"`
}

// IssuesStatus is which of a workspace's installations can take report
// issues.
type IssuesStatus struct {
	// Configured is false when this Loomplane has no GitHub App.
	Configured    bool                 `json:"configured"`
	Installations []InstallationIssues `json:"installations"`
}

// Issue is a GitHub issue.
type Issue struct {
	Number int    `json:"number"`
	URL    string `json:"url"`   // html_url
	State  string `json:"state"` // open or closed
}

// IssueInput opens an issue.
type IssueInput struct {
	Title  string
	Body   string
	Labels []string
}

// IssueEdit changes an issue. A nil Title or Body, or an empty State or
// StateReason, is left as it is.
type IssueEdit struct {
	Title       *string
	Body        *string
	State       string // "", "open" or "closed"
	StateReason string // "", "completed", "not_planned" or "reopened"
}

// Issues are one repository's issues, through a token that can reach
// only them.
type Issues interface {
	// EnsureLabel creates the label, or does nothing when it exists.
	EnsureLabel(ctx context.Context, name, color, description string) error
	// Find returns the first issue with label whose body contains
	// marker, open or closed, opened by this App's own bot user, or nil
	// when there's none. Nobody else's issue counts, a person's or
	// another bot's, so nobody who can label an issue can plant one for
	// Loomplane to take over.
	Find(ctx context.Context, label, marker string) (*Issue, error)
	Create(ctx context.Context, in IssueInput) (*Issue, error)
	// Edit returns ErrNotFound only when the issue is gone.
	Edit(ctx context.Context, number int, in IssueEdit) (*Issue, error)
	Comment(ctx context.Context, number int, body string) error
}

// IssueTracker opens issues on a workspace's repositories, through that
// workspace's own GitHub App installations only.
type IssueTracker interface {
	Access(ctx context.Context, orgID, repo string) (IssueAccess, error)
	// Issues returns ErrIssuesNotAccepted or ErrNoInstallation, with
	// Access's reason, when Access isn't Accepted.
	Issues(ctx context.Context, orgID, repo string) (Issues, error)
}

var _ IssueTracker = (*Connections)(nil)

// IssuesClient returns repo's issues through token, an IssuesToken for
// it.
func (a *App) IssuesClient(token string, repo Repository) Issues {
	return &githubIssues{app: a, token: token, base: a.apiURL + "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)}
}

type githubIssues struct {
	app   *App
	token string
	base  string
}

// issueJSON is an issue, or a pull request, as GitHub lists it.
type issueJSON struct {
	Number      int            `json:"number"`
	HTMLURL     string         `json:"html_url"`
	State       string         `json:"state"`
	Body        string         `json:"body"`
	PullRequest map[string]any `json:"pull_request"`
	User        struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

func (j issueJSON) issue() *Issue { return &Issue{Number: j.Number, URL: j.HTMLURL, State: j.State} }

func (g *githubIssues) EnsureLabel(ctx context.Context, name, color, description string) error {
	var out struct{}
	err := g.app.call(ctx, http.MethodPost, g.base+"/labels", g.token,
		map[string]string{"name": name, "color": color, "description": description}, &out)
	var se *statusError
	if errors.As(err, &se) && se.status == http.StatusUnprocessableEntity && slices.Contains(se.codes, "already_exists") {
		return nil
	}
	return issueError(err)
}

func (g *githubIssues) Find(ctx context.Context, label, marker string) (*Issue, error) {
	for page := 1; page <= maxPages; page++ {
		q := url.Values{
			"labels": {label}, "state": {"all"},
			"per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)},
		}
		var items []issueJSON
		if err := g.app.call(ctx, http.MethodGet, g.base+"/issues?"+q.Encode(), g.token, nil, &items); err != nil {
			return nil, issueError(err)
		}
		for _, it := range items {
			// The issues list has pull requests in it too, and only the
			// App's own issue can be Loomplane's.
			if it.PullRequest == nil && g.ownIssue(it) && strings.Contains(it.Body, marker) {
				return it.issue(), nil
			}
		}
		if len(items) < perPage {
			break
		}
	}
	return nil, nil
}

// ownIssue is whether this App's bot user opened it. An App without a
// slug can't tell, and owns none.
func (g *githubIssues) ownIssue(it issueJSON) bool {
	return g.app.slug != "" && it.User.Type == "Bot" && strings.EqualFold(it.User.Login, g.app.slug+"[bot]")
}

func (g *githubIssues) Create(ctx context.Context, in IssueInput) (*Issue, error) {
	body := map[string]any{"title": in.Title, "body": in.Body}
	if len(in.Labels) > 0 {
		body["labels"] = in.Labels
	}
	var out issueJSON
	if err := g.app.call(ctx, http.MethodPost, g.base+"/issues", g.token, body, &out); err != nil {
		return nil, issueError(err)
	}
	return out.issue(), nil
}

func (g *githubIssues) Edit(ctx context.Context, number int, in IssueEdit) (*Issue, error) {
	body := map[string]any{}
	if in.Title != nil {
		body["title"] = *in.Title
	}
	if in.Body != nil {
		body["body"] = *in.Body
	}
	if in.State != "" {
		body["state"] = in.State
	}
	if in.StateReason != "" {
		body["state_reason"] = in.StateReason
	}
	var out issueJSON
	if err := g.app.call(ctx, http.MethodPatch, g.base+"/issues/"+strconv.Itoa(number), g.token, body, &out); err != nil {
		return nil, issueError(err)
	}
	return out.issue(), nil
}

func (g *githubIssues) Comment(ctx context.Context, number int, body string) error {
	var out struct{}
	err := g.app.call(ctx, http.MethodPost, g.base+"/issues/"+strconv.Itoa(number)+"/comments", g.token,
		map[string]string{"body": body}, &out)
	return issueError(err)
}

// issueError reads an issue call's failure by its status: only 404 and
// 410 mean the issue is gone. A refusal (401, 403, 422) isn't ErrNotFound
// here, so nobody opens a duplicate issue over a permission problem.
func issueError(err error) error {
	var se *statusError
	switch {
	case err == nil || !errors.As(err, &se) || errors.Is(err, ErrRateLimited):
		return err
	case se.status == http.StatusNotFound || se.status == http.StatusGone:
		return ErrNotFound
	default:
		return fmt.Errorf("GitHub refused the issue request with %d %s", se.status, http.StatusText(se.status))
	}
}

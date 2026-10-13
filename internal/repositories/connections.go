package repositories

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// ErrInstallationNotFound means the organization hasn't connected that
// installation.
var ErrInstallationNotFound = errors.New("that GitHub installation isn't connected to this organization")

// Authorizer decides whether a principal may act. *security.Authorizer is
// the real one.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
	Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error)
}

// GitHubApp is what a connection needs from GitHub. *App is the real one.
type GitHubApp interface {
	InstallURL(state string) string
	Installation(ctx context.Context, id int64) (Installation, error)
	VerifyInstaller(ctx context.Context, code string, id int64) error
	RepositoryToken(ctx context.Context, installationID int64, repo string) (string, error)
	Repositories(ctx context.Context, installationID int64) ([]InstalledRepository, error)
	AccountURLs(account, accountType string, id int64) (avatar, manage string)
}

// Auditor records connection changes. Recording must not fail the change.
type Auditor interface {
	Record(ctx context.Context, action string, p security.Principal, inst Installation)
}

// InstallationRecord is one GitHub App installation an organization has
// connected.
type InstallationRecord struct {
	OrgID       string
	ID          int64
	Account     string
	AccountType string
	CreatedBy   string
	CreatedAt   time.Time
}

// Store persists connected installations, per organization.
type Store interface {
	SaveInstallation(ctx context.Context, r *InstallationRecord) error
	ListInstallations(ctx context.Context, orgID string) ([]*InstallationRecord, error)
	// DeleteInstallation returns ErrInstallationNotFound when there was
	// nothing to delete.
	DeleteInstallation(ctx context.Context, orgID string, id int64) error
}

// GitHubInstallation is a connected installation, as the API shows it.
type GitHubInstallation struct {
	// ID is the installation's GitHub ID, as a string so it's a stable key
	// in the dashboard's cache.
	ID          string `json:"id" forge:"id"`
	Account     string `json:"account"`
	AccountType string `json:"account_type"`
	// AvatarURL is the account's picture, and ManageURL the page on GitHub
	// where its owner chooses which repositories the App can read.
	AvatarURL string    `json:"avatar_url"`
	ManageURL string    `json:"manage_url"`
	CreatedAt time.Time `json:"created_at"`
}

// GitHubStatus is the organization's GitHub connection.
type GitHubStatus struct {
	// Configured is false when this Loomplane has no GitHub App, so only
	// public repositories can be read.
	Configured bool `json:"configured"`
	// InstallURL starts connecting another account. It's empty for people
	// who may not connect one.
	InstallURL    string               `json:"install_url,omitempty"`
	Installations []GitHubInstallation `json:"installations"`
}

// ConnectGitHubInput is what GitHub's install redirect carries back.
type ConnectGitHubInput struct {
	InstallationID int64  `json:"installation_id"`
	Code           string `json:"code"`
	State          string `json:"state"`
}

// Connections manages an organization's GitHub App installations, and
// mints the tokens analyses read private repositories with.
type Connections struct {
	store    Store
	app      GitHubApp
	authz    Authorizer
	audit    Auditor
	stateKey []byte
	now      func() time.Time
}

// NewConnections returns Connections. app is nil when no GitHub App is
// configured; audit may be nil.
func NewConnections(store Store, app GitHubApp, authz Authorizer, audit Auditor, stateKey []byte) *Connections {
	return &Connections{store: store, app: app, authz: authz, audit: audit, stateKey: stateKey, now: time.Now}
}

// Status returns the organization's connection, and a link to connect
// another account for those who may.
func (c *Connections) Status(ctx context.Context, p security.Principal) (*GitHubStatus, error) {
	if err := c.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	st := &GitHubStatus{Configured: c.app != nil, Installations: []GitHubInstallation{}}
	if c.app == nil {
		return st, nil
	}
	recs, err := c.store.ListInstallations(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		st.Installations = append(st.Installations, c.view(r))
	}
	may, err := c.authz.Allowed(ctx, p, security.RepositoryConnect)
	if err != nil {
		return nil, err
	}
	if may {
		state, err := SignState(c.stateKey, p.OrgID, p.UserID, c.now())
		if err != nil {
			return nil, err
		}
		st.InstallURL = c.app.InstallURL(state)
	}
	return st, nil
}

// Connect links an installation to the caller's organization, once
// GitHub's redirect proves the caller started it and can see it.
func (c *Connections) Connect(ctx context.Context, p security.Principal, in ConnectGitHubInput) (*GitHubInstallation, error) {
	if err := c.authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return nil, err
	}
	if c.app == nil {
		return nil, ErrAppNotConfigured
	}
	st, err := VerifyState(c.stateKey, in.State, c.now())
	if err != nil {
		return nil, err
	}
	if st.OrgID != p.OrgID || st.UserID != p.UserID {
		return nil, ErrBadState
	}
	if in.InstallationID <= 0 {
		return nil, fmt.Errorf("%w: no installation", ErrBadState)
	}
	err = c.app.VerifyInstaller(ctx, in.Code, in.InstallationID)
	if err != nil {
		return nil, err
	}
	inst, err := c.app.Installation(ctx, in.InstallationID)
	if err != nil {
		return nil, err
	}
	rec := &InstallationRecord{
		OrgID: p.OrgID, ID: inst.ID, Account: inst.Account, AccountType: inst.AccountType,
		CreatedBy: p.UserID, CreatedAt: c.now().UTC(),
	}
	err = c.store.SaveInstallation(ctx, rec)
	if err != nil {
		return nil, err
	}
	if c.audit != nil {
		c.audit.Record(ctx, "github.connect", p, inst)
	}
	v := c.view(rec)
	return &v, nil
}

// Disconnect unlinks an installation from the organization. The App stays
// installed on GitHub; uninstalling it is done there.
func (c *Connections) Disconnect(ctx context.Context, p security.Principal, installationID string) error {
	if err := c.authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return err
	}
	id, err := strconv.ParseInt(installationID, 10, 64)
	if err != nil {
		return ErrInstallationNotFound
	}
	err = c.store.DeleteInstallation(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if c.audit != nil {
		c.audit.Record(ctx, "github.disconnect", p, Installation{ID: id})
	}
	return nil
}

// ConnectedRepository is a repository one of the organization's
// installations can read.
type ConnectedRepository struct {
	InstalledRepository
	Installation string `json:"installation"`
}

// Repositories lists what the organization's installations can read.
func (c *Connections) Repositories(ctx context.Context, p security.Principal) ([]ConnectedRepository, error) {
	if err := c.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	if c.app == nil {
		return []ConnectedRepository{}, nil
	}
	recs, err := c.store.ListInstallations(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	out := []ConnectedRepository{}
	for _, r := range recs {
		repos, err := c.app.Repositories(ctx, r.ID)
		if err != nil {
			return nil, fmt.Errorf("repositories of %s: %w", r.Account, err)
		}
		for _, repo := range repos {
			out = append(out, ConnectedRepository{InstalledRepository: repo, Installation: strconv.FormatInt(r.ID, 10)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].FullName) < strings.ToLower(out[j].FullName) })
	return out, nil
}

// Token mints a token that reads repo for an analysis in orgID, from the
// organization's installation on the repository's owner. It returns ""
// when there's no such installation, so a public repository is still
// read anonymously. Only the organization's own installations are ever
// used: another organization's can't read this one's code.
func (c *Connections) Token(ctx context.Context, orgID string, repo Repository) (string, error) {
	if c.app == nil {
		return "", nil
	}
	recs, err := c.store.ListInstallations(ctx, orgID)
	if err != nil {
		return "", err
	}
	for _, r := range recs {
		if !strings.EqualFold(r.Account, repo.Owner) {
			continue
		}
		tok, err := c.app.RepositoryToken(ctx, r.ID, repo.Name)
		if errors.Is(err, ErrNotFound) {
			// The installation can't see this repository, or it's gone.
			return "", nil
		}
		return tok, err
	}
	return "", nil
}

func (c *Connections) view(r *InstallationRecord) GitHubInstallation {
	avatar, manage := c.app.AccountURLs(r.Account, r.AccountType, r.ID)
	return GitHubInstallation{
		ID: strconv.FormatInt(r.ID, 10), Account: r.Account, AccountType: r.AccountType,
		AvatarURL: avatar, ManageURL: manage, CreatedAt: r.CreatedAt.UTC(),
	}
}

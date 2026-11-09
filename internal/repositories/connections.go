package repositories

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	IssuesToken(ctx context.Context, installationID int64, repo string) (string, error)
	IssuesClient(token string, repo Repository) Issues
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
	// Permissions are what the installation had accepted when Loomplane
	// last read them; nil when it never has. PermissionsCheckedAt is when
	// Loomplane last asked GitHub, which may be a failed attempt after
	// that read: a failure keeps the permissions and moves only the time.
	Permissions          map[string]string
	PermissionsCheckedAt *time.Time
}

// Store persists connected installations, per organization.
type Store interface {
	SaveInstallation(ctx context.Context, r *InstallationRecord) error
	ListInstallations(ctx context.Context, orgID string) ([]*InstallationRecord, error)
	// DeleteInstallation returns ErrInstallationNotFound when there was
	// nothing to delete.
	DeleteInstallation(ctx context.Context, orgID string, id int64) error
	// SavePermissions records what an installation had accepted at at.
	SavePermissions(ctx context.Context, orgID string, id int64, perms map[string]string, at time.Time) error
	// DeferPermissionsCheck records a failed read of an installation's
	// permissions at at, so the next read waits PermissionsTTL. The
	// permissions stay as they were. It does nothing for an installation
	// whose permissions were never read, or were checked after at.
	DeferPermissionsCheck(ctx context.Context, orgID string, id int64, at time.Time) error
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
	warn     func(msg string, err error)
}

// SetWarn sets where failures that don't fail the call are reported. No
// error it's given holds a token.
func (c *Connections) SetWarn(fn func(msg string, err error)) { c.warn = fn }

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
	if st.OrgID != p.OrgID || st.UserID != p.UserID || st.Workspace != "" {
		// A state naming a workspace was made for Bitbucket.
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

// Access says whether Loomplane can open issues on repo, a repository
// key, for orgID: through the organization's own installation on the
// repository's owner, once that installation has accepted the Issues
// permission. An installation's permissions are read from GitHub at most
// every PermissionsTTL. When GitHub can't be asked, the last ones read
// still count, and the error is returned only when there are none.
func (c *Connections) Access(ctx context.Context, orgID, repo string) (IssueAccess, error) {
	acc, _, err := c.access(ctx, orgID, repo)
	return acc, err
}

// Issues returns repo's issues through a token that can reach only
// them. When Access isn't Accepted, it returns ErrIssuesNotAccepted for
// an installation that hasn't accepted the permission, and
// ErrNoInstallation otherwise, each with Access's reason.
func (c *Connections) Issues(ctx context.Context, orgID, repo string) (Issues, error) {
	acc, r, err := c.access(ctx, orgID, repo)
	if err != nil {
		return nil, err
	}
	if !acc.Accepted {
		if acc.InstallationID != 0 {
			return nil, fmt.Errorf("%w: %s", ErrIssuesNotAccepted, acc.Reason)
		}
		return nil, fmt.Errorf("%w: %s", ErrNoInstallation, acc.Reason)
	}
	tok, err := c.app.IssuesToken(ctx, acc.InstallationID, r.Name)
	var se *statusError
	if errors.As(err, &se) && (se.status == http.StatusNotFound || se.status == http.StatusUnprocessableEntity) {
		// The installation can't see the repository, is gone, or no
		// longer has the permission.
		reason := acc.Account + "'s GitHub App installation can't reach " + r.FullName() +
			". An owner of " + acc.Account + " can choose which repositories the App can read on GitHub."
		return nil, fmt.Errorf("%w: %s", ErrNoInstallation, reason)
	}
	if err != nil {
		return nil, fmt.Errorf("mint an issues token for %s: %w", r.FullName(), issueError(err))
	}
	return c.app.IssuesClient(tok, r), nil
}

func (c *Connections) access(ctx context.Context, orgID, key string) (IssueAccess, Repository, error) {
	acc := IssueAccess{Repository: key}
	if c.app == nil {
		acc.Reason = "This Loomplane has no GitHub App, so reports go to Herald only"
		return acc, Repository{}, nil
	}
	repo, err := ParseKey(key)
	if err != nil {
		// Asking again won't make the key readable, so the report is
		// delivered without an issue rather than retried.
		acc.Reason = "Loomplane couldn't read the repository " + key + ", so reports go to Herald only"
		return acc, Repository{}, nil
	}
	if !repo.onGitHub() {
		acc.Reason = "Loomplane opens issues on GitHub repositories only, so reports for " + key + " go to Herald only"
		return acc, repo, nil
	}
	acc.Repository = repo.Key()
	recs, err := c.store.ListInstallations(ctx, orgID)
	if err != nil {
		return acc, repo, err
	}
	var rec *InstallationRecord
	for _, r := range recs {
		if strings.EqualFold(r.Account, repo.Owner) {
			rec = r
			break
		}
	}
	if rec == nil {
		acc.Reason = "No GitHub App installation on " + repo.Owner + " is connected to this workspace"
		return acc, repo, nil
	}
	_, acc.ManageURL = c.app.AccountURLs(rec.Account, rec.AccountType, rec.ID)
	acc.InstallationID, acc.Account = rec.ID, rec.Account
	if err := c.permissions(ctx, rec); err != nil {
		return acc, repo, err
	}
	if rec.Permissions[IssuesPermission] != "write" {
		acc.Reason = rec.Account + " hasn't accepted the Issues permission yet. An owner of " + rec.Account + " can review the App's permissions on GitHub."
		return acc, repo, nil
	}
	acc.Accepted = true
	return acc, repo, nil
}

// permissions brings rec's permissions up to date when they're older
// than PermissionsTTL. When GitHub can't be asked it keeps the stored
// ones, reports the failure, and records the attempt, so the next read
// waits PermissionsTTL too rather than asking GitHub on every call
// through an outage. It fails only when none were ever read.
func (c *Connections) permissions(ctx context.Context, rec *InstallationRecord) error {
	now := c.now().UTC()
	if rec.PermissionsCheckedAt != nil && now.Sub(*rec.PermissionsCheckedAt) < PermissionsTTL {
		return nil
	}
	inst, err := c.app.Installation(ctx, rec.ID)
	if err != nil {
		err = fmt.Errorf("read %s's GitHub App permissions: %w", rec.Account, err)
		if rec.PermissionsCheckedAt == nil {
			return err
		}
		c.warnf("keep the last known GitHub App permissions of "+rec.Account, err)
		if derr := c.store.DeferPermissionsCheck(ctx, rec.OrgID, rec.ID, now); derr != nil {
			c.warnf("record the failed read of "+rec.Account+"'s GitHub App permissions", derr)
		} else {
			rec.PermissionsCheckedAt = &now
		}
		return nil
	}
	if err := c.store.SavePermissions(ctx, rec.OrgID, rec.ID, inst.Permissions, now); err != nil {
		return err
	}
	rec.Permissions, rec.PermissionsCheckedAt = inst.Permissions, &now
	return nil
}

// IssuesStatus says which of the organization's installations have
// accepted the Issues permission, so AI settings can name the ones that
// haven't. An installation whose permissions GitHub can't tell yet shows
// as not accepted.
func (c *Connections) IssuesStatus(ctx context.Context, p security.Principal) (*IssuesStatus, error) {
	if err := c.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	st := &IssuesStatus{Configured: c.app != nil, Installations: []InstallationIssues{}}
	if c.app == nil {
		return st, nil
	}
	recs, err := c.store.ListInstallations(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if err := c.permissions(ctx, r); err != nil {
			c.warnf("refresh the Issues status of "+r.Account, err)
			if r.PermissionsCheckedAt == nil {
				// GitHub couldn't be asked, and nothing was ever read: the
				// row shows as not accepted, without a check time.
				r.Permissions = nil
			}
		}
		_, manage := c.app.AccountURLs(r.Account, r.AccountType, r.ID)
		st.Installations = append(st.Installations, InstallationIssues{
			Account: r.Account, AccountType: r.AccountType, Accepted: r.Permissions[IssuesPermission] == "write",
			ManageURL: manage, CheckedAt: r.PermissionsCheckedAt,
		})
	}
	return st, nil
}

func (c *Connections) warnf(msg string, err error) {
	if c.warn != nil && err != nil {
		c.warn(msg, err)
	}
}

func (c *Connections) view(r *InstallationRecord) GitHubInstallation {
	avatar, manage := c.app.AccountURLs(r.Account, r.AccountType, r.ID)
	return GitHubInstallation{
		ID: strconv.FormatInt(r.ID, 10), Account: r.Account, AccountType: r.AccountType,
		AvatarURL: avatar, ManageURL: manage, CreatedAt: r.CreatedAt.UTC(),
	}
}

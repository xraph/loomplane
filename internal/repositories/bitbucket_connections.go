package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

// ErrBitbucketNotConfigured means this Loomplane has no Bitbucket OAuth
// consumer, so it reads public Bitbucket repositories only.
var ErrBitbucketNotConfigured = errors.New("this Loomplane has no Bitbucket OAuth consumer configured, so it reads public Bitbucket repositories only")

// ErrBadWorkspace means what was typed can't be a Bitbucket workspace slug.
var ErrBadWorkspace = errors.New("that isn't a Bitbucket workspace: use the part after bitbucket.org/, such as acme")

// ErrBitbucketSecretMissing is what Secrets.Reveal returns when the
// workspace's stored sign-in is gone. Renewing treats it like a refusal:
// there is nothing left to renew from, so the workspace needs connecting
// again.
var ErrBitbucketSecretMissing = errors.New("the stored Bitbucket sign-in is missing")

// Why a workspace needs connecting again, as the card shows it.
const (
	bitbucketRefusedMessage       = "Bitbucket no longer accepts this workspace's sign-in. Connect the workspace again."
	bitbucketConnectorLeftMessage = "The member who connected this workspace has left the organization. Connect the workspace again."
	bitbucketUnreadableMessage    = "Loomplane can't read this workspace's stored sign-in. Connect the workspace again."
	bitbucketAccountLeftMessage   = "The Bitbucket account that connected this workspace is no longer a member of it. Connect the workspace again."
)

// bitbucketRenewAhead is how close to expiry a cached access token is
// renewed. A 401 on a read looks like a missing repository, so a token is
// never handed out this close to expiring.
const bitbucketRenewAhead = time.Minute

// bitbucketRenewTimeout bounds asking Bitbucket for a new access token, and
// keeping the refresh token that comes back, once neither may be abandoned
// with the caller.
const bitbucketRenewTimeout = 30 * time.Second

// maxBitbucketRenewals caps the renewals one process runs against the
// database at once, as cloud sign-ins do: each holds a pooled connection
// for its lock.
const maxBitbucketRenewals = 4

// BitbucketAPI is what workspace connections need from Bitbucket.
// *BitbucketOAuth is the real one.
type BitbucketAPI interface {
	AuthorizeURL(state string) string
	Exchange(ctx context.Context, code string) (BitbucketGrant, error)
	Refresh(ctx context.Context, refreshToken string) (BitbucketGrant, error)
	CurrentUser(ctx context.Context, token string) (BitbucketUser, error)
	Membership(ctx context.Context, token, slug string) (BitbucketWorkspace, error)
	Workspace(ctx context.Context, token, uuid string) (BitbucketWorkspace, error)
	Repositories(ctx context.Context, token, workspaceSlug string) ([]BitbucketRepository, error)
}

var (
	_ BitbucketAPI = (*BitbucketOAuth)(nil)
	_ TokenSource  = (*BitbucketConnections)(nil)
)

// Secrets keeps a credential per organization and connection. The
// connections package's *VaultSecrets is the real one.
type Secrets interface {
	Put(ctx context.Context, orgID, connectionID string, credential []byte) error
	Reveal(ctx context.Context, orgID, connectionID string) ([]byte, error)
	Delete(ctx context.Context, orgID, connectionID string) error
}

// BitbucketAuditor records workspace connections. Recording must not fail
// the change. The connection holds no token.
type BitbucketAuditor interface {
	RecordBitbucket(ctx context.Context, action string, p security.Principal, c BitbucketConnection)
}

// storedGrant is what Vault holds for a workspace: the refresh token and
// nothing else. The access token only ever lives in memory.
type storedGrant struct {
	RefreshToken string `json:"refresh_token"`
}

// Format keeps the token out of anything printed by mistake.
func (storedGrant) Format(f fmt.State, _ rune) {
	if _, err := io.WriteString(f, "storedGrant{redacted}"); err != nil {
		return // fmt has nowhere to report it either
	}
}

// BitbucketWorkspaceView is a connected workspace, as the API shows it.
type BitbucketWorkspaceView struct {
	// ID is Loomplane's, not Bitbucket's.
	ID string `json:"id" forge:"id"`
	// Workspace is the slug, the acme in bitbucket.org/acme.
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	// ManageURL is the workspace's settings on Bitbucket.
	ManageURL string `json:"manage_url"`
	// ConnectedBy is the Bitbucket account whose sign-in is stored.
	ConnectedBy string `json:"connected_by"`
	// Status is ready or reconnect_needed, and Message says why it's the
	// latter.
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// BitbucketStatus is the organization's Bitbucket connection.
type BitbucketStatus struct {
	// Configured is false when this Loomplane has no Bitbucket consumer, so
	// only public repositories can be read.
	Configured bool `json:"configured"`
	// CanConnect says whether the caller may connect a workspace.
	CanConnect bool                     `json:"can_connect"`
	Workspaces []BitbucketWorkspaceView `json:"workspaces"`
	// Problems names each workspace that needs connecting again.
	Problems []string `json:"problems"`
}

// BitbucketConnectInput is what Bitbucket's callback carries back.
type BitbucketConnectInput struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

// ConnectedBitbucketRepository is a repository one of the organization's
// workspaces can read.
type ConnectedBitbucketRepository struct {
	BitbucketRepository
	// Workspace is the connection's ID.
	Workspace string `json:"workspace"`
}

// BitbucketRepositoryList is what the organization's workspaces can read.
// Problems names each workspace that couldn't be listed, so one failing
// doesn't hide the rest.
type BitbucketRepositoryList struct {
	Repositories []ConnectedBitbucketRepository `json:"repositories"`
	Problems     []string                       `json:"problems"`
}

// cachedToken is an access token, when it stops working, and the grant
// generation of the row it came from. A reconnect keeps the row's ID but
// moves the generation on, so every process can tell a token from the old
// sign-in.
type cachedToken struct {
	token      string
	expires    time.Time
	generation int64
}

// BitbucketConnections manages the Bitbucket workspaces an organization
// has connected, and mints the tokens analyses read private repositories
// with. A workspace's refresh token is in Vault; its access token is only
// ever in memory here.
type BitbucketConnections struct {
	store        BitbucketStore
	api          BitbucketAPI
	secrets      Secrets
	authz        Authorizer
	audit        BitbucketAuditor
	stateKey     []byte
	now          func() time.Time
	warn         func(msg string, err error)
	onDisconnect func(ctx context.Context, orgID, connectionID string)
	renewals     *bitbucketGate

	mu     sync.Mutex
	tokens map[string]cachedToken // by orgID/connectionID
}

// NewBitbucketConnections returns BitbucketConnections. api is nil when
// Bitbucket isn't configured; audit may be nil. stateKey is derived with
// BitbucketStateKeyLabel.
func NewBitbucketConnections(store BitbucketStore, api BitbucketAPI, secrets Secrets, authz Authorizer, audit BitbucketAuditor, stateKey []byte) *BitbucketConnections {
	return &BitbucketConnections{
		store: store, api: api, secrets: secrets, authz: authz, audit: audit, stateKey: stateKey,
		now: time.Now, renewals: newBitbucketGate(maxBitbucketRenewals), tokens: map[string]cachedToken{},
	}
}

// SetOnDisconnect sets what runs when a workspace is disconnected, before
// its row goes, such as removing its webhooks. It can't stop the
// disconnect.
func (c *BitbucketConnections) SetOnDisconnect(fn func(ctx context.Context, orgID, connectionID string)) {
	c.onDisconnect = fn
}

// SetWarn sets where failures that don't fail the call are reported. No
// error it's given holds a token.
func (c *BitbucketConnections) SetWarn(fn func(msg string, err error)) { c.warn = fn }

// Status returns the organization's workspaces, and whether the caller may
// connect another.
func (c *BitbucketConnections) Status(ctx context.Context, p security.Principal) (*BitbucketStatus, error) {
	if err := c.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	st := &BitbucketStatus{Configured: c.api != nil, Workspaces: []BitbucketWorkspaceView{}, Problems: []string{}}
	if c.api == nil {
		return st, nil
	}
	rows, err := c.store.ListBitbucketWorkspaces(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		st.Workspaces = append(st.Workspaces, bitbucketView(r))
		if r.Status != BitbucketReady {
			st.Problems = append(st.Problems, fmt.Sprintf("%s needs connecting again: %s", r.WorkspaceSlug, r.Message))
		}
	}
	if st.CanConnect, err = c.authz.Allowed(ctx, p, security.RepositoryConnect); err != nil {
		return nil, err
	}
	return st, nil
}

// Authorize returns the Bitbucket link that starts connecting workspace to
// the caller's organization. The state in it says who asked, for which
// organization and workspace.
func (c *BitbucketConnections) Authorize(ctx context.Context, p security.Principal, workspace string) (string, error) {
	if err := c.authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return "", err
	}
	if c.api == nil {
		return "", ErrBitbucketNotConfigured
	}
	if !bitbucketSlug.MatchString(workspace) {
		return "", ErrBadWorkspace
	}
	state, err := SignWorkspaceState(c.stateKey, p.OrgID, p.UserID, workspace, c.now())
	if err != nil {
		return "", err
	}
	return c.api.AuthorizeURL(state), nil
}

// Connect links a workspace to the caller's organization once Bitbucket's
// callback proves the caller started it, and the account that signed in
// belongs to the workspace. Connecting one the organization already has,
// such as one that needs connecting again, keeps its ID and replaces its
// sign-in.
func (c *BitbucketConnections) Connect(ctx context.Context, p security.Principal, in BitbucketConnectInput) (*BitbucketWorkspaceView, error) {
	if err := c.authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return nil, err
	}
	if c.api == nil {
		return nil, ErrBitbucketNotConfigured
	}
	// A callback missing either half proves nothing, so Bitbucket isn't
	// asked about it.
	if strings.TrimSpace(in.Code) == "" || strings.TrimSpace(in.State) == "" {
		return nil, ErrBadState
	}
	st, err := VerifyState(c.stateKey, in.State, c.now())
	if err != nil {
		return nil, err
	}
	if st.OrgID != p.OrgID || st.UserID != p.UserID || st.Workspace == "" {
		return nil, ErrBadState
	}
	grant, err := c.api.Exchange(ctx, in.Code)
	if err != nil {
		return nil, err
	}
	if grant.RefreshToken == "" {
		// Without one the workspace would stop working in two hours, with
		// nothing to renew it from.
		return nil, ErrBitbucketRefused
	}
	// Not a member: the grant is dropped here, and nothing is kept.
	ws, err := c.api.Membership(ctx, grant.AccessToken, st.Workspace)
	if err != nil {
		return nil, err
	}
	if ws.UUID == "" || !bitbucketSlug.MatchString(ws.Slug) {
		// The UUID is what the row is keyed on, so a workspace without
		// one can't be kept apart from another.
		return nil, errors.New("Bitbucket didn't say which workspace that is; try connecting again")
	}
	user, err := c.api.CurrentUser(ctx, grant.AccessToken)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	row := &BitbucketConnection{
		OrgID: p.OrgID, WorkspaceUUID: ws.UUID, WorkspaceSlug: ws.Slug, WorkspaceName: ws.Name, AvatarURL: ws.AvatarURL,
		BitbucketUserUUID: user.UUID, BitbucketUserName: user.Name, Status: BitbucketReady,
		CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now,
	}
	existing, err := c.byUUID(ctx, p.OrgID, ws.UUID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		row.ID = existing.ID
	} else {
		tid, genErr := typeid.Generate("bbws")
		if genErr != nil {
			return nil, fmt.Errorf("generate Bitbucket workspace id: %w", genErr)
		}
		row.ID = tid.String()
	}
	id := row.ID
	err = c.store.WithBitbucketLock(ctx, p.OrgID, id, func() error {
		return c.replaceGrant(ctx, row, grant, existing != nil)
	})
	if err != nil {
		return nil, err
	}
	if row.ID != id {
		// Someone else in the organization connected the same workspace a
		// moment earlier. The row is theirs, now holding this sign-in, so
		// the secret moves to it.
		err = c.store.WithBitbucketLock(ctx, p.OrgID, row.ID, func() error {
			if putErr := c.putRefresh(ctx, p.OrgID, row.ID, grant.RefreshToken); putErr != nil {
				return putErr
			}
			c.remember(row, grant)
			return nil
		})
		c.forget(p.OrgID, id)
		if delErr := c.secrets.Delete(ctx, p.OrgID, id); delErr != nil {
			c.warnf("remove the duplicate Bitbucket sign-in of "+id, delErr)
		}
		if err != nil {
			return nil, err
		}
	}
	if c.audit != nil {
		c.audit.RecordBitbucket(ctx, "bitbucket.connect", p, *row)
	}
	v := bitbucketView(row)
	return &v, nil
}

// replaceGrant stores row and its sign-in, holding row's lock. If the row
// can't be saved, Vault gets back what it held, so the row and its secret
// never belong to two different sign-ins.
func (c *BitbucketConnections) replaceGrant(ctx context.Context, row *BitbucketConnection, grant BitbucketGrant, existed bool) error {
	var old []byte
	if existed {
		// A secret that can't be read can't be restored either, and the
		// new one replaces it all the same.
		if prev, err := c.secrets.Reveal(ctx, row.OrgID, row.ID); err == nil {
			old = prev
		}
	}
	if err := c.putRefresh(ctx, row.OrgID, row.ID, grant.RefreshToken); err != nil {
		return err
	}
	id := row.ID
	if err := c.store.SaveBitbucketWorkspace(ctx, row); err != nil {
		var undo error
		if old != nil {
			undo = c.secrets.Put(ctx, row.OrgID, id, old)
		} else {
			undo = c.secrets.Delete(ctx, row.OrgID, id)
		}
		if undo != nil {
			c.warnf("restore the Bitbucket sign-in of "+id, undo)
		}
		return err
	}
	// The old sign-in's access token goes, and the new one is good for
	// as long as Bitbucket says.
	c.forget(row.OrgID, id)
	if row.ID == id {
		c.remember(row, grant)
	}
	return nil
}

// Disconnect unlinks a workspace from the organization and deletes its
// sign-in. The consumer stays approved on Bitbucket; revoking it is done
// there, under Personal settings.
func (c *BitbucketConnections) Disconnect(ctx context.Context, p security.Principal, id string) error {
	if err := c.authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return err
	}
	row, err := c.store.GetBitbucketWorkspace(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	// It may want a token, so it runs before the lock, and before the row
	// and its sign-in go.
	if c.onDisconnect != nil {
		c.onDisconnect(ctx, p.OrgID, id)
	}
	var vaultErr error
	err = c.store.WithBitbucketLock(ctx, p.OrgID, id, func() error {
		if delErr := c.store.DeleteBitbucketWorkspace(ctx, p.OrgID, id); delErr != nil {
			return delErr
		}
		c.forget(p.OrgID, id)
		// The row is gone, so a caller giving up now would leave the
		// refresh token where nothing reaches it.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bitbucketRenewTimeout)
		defer cancel()
		vaultErr = c.secrets.Delete(dctx, p.OrgID, id)
		return nil
	})
	if err != nil {
		return err
	}
	if c.audit != nil {
		c.audit.RecordBitbucket(ctx, "bitbucket.disconnect", p, *row)
	}
	if vaultErr != nil {
		return fmt.Errorf("the workspace is disconnected, but its sign-in couldn't be removed from Vault: %w", vaultErr)
	}
	return nil
}

// Repositories lists what the organization's ready workspaces can read. A
// workspace that can't be listed is named in Problems instead.
func (c *BitbucketConnections) Repositories(ctx context.Context, p security.Principal) (*BitbucketRepositoryList, error) {
	if err := c.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	out := &BitbucketRepositoryList{Repositories: []ConnectedBitbucketRepository{}, Problems: []string{}}
	if c.api == nil {
		return out, nil
	}
	rows, err := c.store.ListBitbucketWorkspaces(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Status != BitbucketReady {
			out.Problems = append(out.Problems, fmt.Sprintf("%s needs connecting again: %s", r.WorkspaceSlug, r.Message))
			continue
		}
		cur, tok, err := c.mint(ctx, r)
		switch {
		case err != nil:
			c.warnf("renew the Bitbucket sign-in of "+r.ID, err)
			out.Problems = append(out.Problems, fmt.Sprintf("Loomplane couldn't renew the sign-in for %s, so its repositories aren't listed. Try again shortly.", r.WorkspaceSlug))
			continue
		case cur == nil:
			continue // disconnected meanwhile
		case tok == "":
			out.Problems = append(out.Problems, fmt.Sprintf("%s needs connecting again: %s", cur.WorkspaceSlug, cur.Message))
			continue
		}
		repos, err := c.api.Repositories(ctx, tok, cur.WorkspaceSlug)
		if err != nil {
			out.Problems = append(out.Problems, fmt.Sprintf("Bitbucket didn't list the repositories in %s: %v", cur.WorkspaceSlug, err))
			continue
		}
		for _, repo := range repos {
			out.Repositories = append(out.Repositories, ConnectedBitbucketRepository{BitbucketRepository: repo, Workspace: cur.ID})
		}
	}
	sort.Slice(out.Repositories, func(i, j int) bool {
		return strings.ToLower(out.Repositories[i].FullName) < strings.ToLower(out.Repositories[j].FullName)
	})
	return out, nil
}

// Token returns an access token that reads repo for an analysis in orgID,
// from the organization's connection to the repository's workspace. Only
// the organization's own connections are used, only the one whose slug is
// the repository's owner, and never one that needs connecting again.
// Anything else is "", so a public repository is still read anonymously.
func (c *BitbucketConnections) Token(ctx context.Context, orgID string, repo Repository) (string, error) {
	if c.api == nil || !strings.EqualFold(repo.Host, HostBitbucket) || repo.Owner == "" {
		return "", nil
	}
	row, err := c.find(ctx, orgID, repo.Owner, true)
	if err != nil || row == nil {
		return "", err
	}
	cur, tok, err := c.mint(ctx, row)
	if err != nil {
		return "", err
	}
	// A renewal may have found the workspace renamed, and the old slug
	// could by now be someone else's.
	if cur == nil || !strings.EqualFold(cur.WorkspaceSlug, repo.Owner) {
		return "", nil
	}
	return tok, nil
}

// ConnectionFor returns the organization's connection to workspace (a
// slug) and a token to act in it with. The token is "" when the connection
// needs connecting again, or was renamed away from that slug. No
// connection is ErrWorkspaceNotFound.
func (c *BitbucketConnections) ConnectionFor(ctx context.Context, orgID, workspace string) (*BitbucketConnection, string, error) {
	if c.api == nil {
		return nil, "", ErrBitbucketNotConfigured
	}
	row, err := c.find(ctx, orgID, workspace, false)
	if err != nil {
		return nil, "", err
	}
	if row == nil {
		return nil, "", ErrWorkspaceNotFound
	}
	if row.Status != BitbucketReady {
		return row, "", nil
	}
	cur, tok, err := c.mint(ctx, row)
	if err != nil {
		return nil, "", err
	}
	if cur == nil {
		return nil, "", ErrWorkspaceNotFound
	}
	if !strings.EqualFold(cur.WorkspaceSlug, workspace) {
		return cur, "", nil
	}
	return cur, tok, nil
}

// find returns the organization's connection whose slug is slug, or nil.
// With readyOnly, one that needs connecting again doesn't count.
func (c *BitbucketConnections) find(ctx context.Context, orgID, slug string, readyOnly bool) (*BitbucketConnection, error) {
	rows, err := c.store.ListBitbucketWorkspaces(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var match *BitbucketConnection
	for _, r := range rows {
		if r.OrgID != orgID || !strings.EqualFold(r.WorkspaceSlug, slug) {
			continue
		}
		if r.Status == BitbucketReady {
			return r, nil
		}
		if !readyOnly && match == nil {
			match = r
		}
	}
	return match, nil
}

// byUUID returns the organization's connection to the workspace with that
// UUID, or nil.
func (c *BitbucketConnections) byUUID(ctx context.Context, orgID, uuid string) (*BitbucketConnection, error) {
	rows, err := c.store.ListBitbucketWorkspaces(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.WorkspaceUUID == uuid {
			return r, nil
		}
	}
	return nil, nil
}

// mint returns a usable access token for row, from memory while it has
// more than a minute left, or else renewed. It also returns the connection
// as it stands after any renewal, or nil if it was disconnected meanwhile.
// The token is "" when the connection needs connecting again.
//
// Renewal queues callers in this process first, so at most one per
// connection waits on the database. It then takes the connection's lock
// and reads the row and Vault again, so of several callers in any process
// only one spends a refresh token, and the rest use what it stored.
func (c *BitbucketConnections) mint(ctx context.Context, row *BitbucketConnection) (*BitbucketConnection, string, error) {
	if tok, ok := c.cached(row); ok {
		return row, tok, nil
	}
	leave, err := c.renewals.enter(ctx, row.OrgID+"/"+row.ID)
	if err != nil {
		return nil, "", err
	}
	defer leave()
	// Whoever went first has most likely renewed it already.
	if tok, ok := c.cached(row); ok {
		return row, tok, nil
	}
	var cur *BitbucketConnection
	var tok string
	err = c.store.WithBitbucketLock(ctx, row.OrgID, row.ID, func() error {
		got, getErr := c.store.GetBitbucketWorkspace(ctx, row.OrgID, row.ID)
		if errors.Is(getErr, ErrWorkspaceNotFound) {
			return nil // disconnected meanwhile
		}
		if getErr != nil {
			return getErr
		}
		cur = got
		if cur.Status != BitbucketReady {
			return nil
		}
		var renewErr error
		tok, renewErr = c.renew(ctx, cur)
		return renewErr
	})
	if err != nil {
		return nil, "", err
	}
	return cur, tok, nil
}

// renew trades cur's refresh token for an access token, holding cur's
// lock. It checks first that the member whose sign-in it is still belongs
// to the organization. A refusal, a member who left, or a sign-in that
// can't be read or is gone from Vault marks cur reconnect_needed and gives "". Anything else
// that goes wrong is an error and changes nothing.
func (c *BitbucketConnections) renew(ctx context.Context, cur *BitbucketConnection) (string, error) {
	member, err := c.store.BitbucketConnectorIsMember(ctx, cur.OrgID, cur.CreatedBy)
	if err != nil {
		return "", err
	}
	if !member {
		return "", c.needReconnect(ctx, cur, bitbucketConnectorLeftMessage)
	}
	raw, err := c.secrets.Reveal(ctx, cur.OrgID, cur.ID)
	if errors.Is(err, ErrBitbucketSecretMissing) {
		return "", c.needReconnect(ctx, cur, bitbucketUnreadableMessage)
	}
	if err != nil {
		return "", fmt.Errorf("read the Bitbucket sign-in for %s: %w", cur.WorkspaceSlug, err)
	}
	var held storedGrant
	if json.Unmarshal(raw, &held) != nil || held.RefreshToken == "" {
		// The decode error could quote the secret, so it isn't kept.
		return "", c.needReconnect(ctx, cur, bitbucketUnreadableMessage)
	}
	// Bitbucket may spend the refresh token before its answer is read, so
	// a caller giving up must not cut the answer off, or stop the new
	// refresh token being kept.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bitbucketRenewTimeout)
	defer cancel()
	g, err := c.api.Refresh(rctx, held.RefreshToken)
	if errors.Is(err, ErrBitbucketRefused) {
		return "", c.needReconnect(ctx, cur, bitbucketRefusedMessage)
	}
	if err != nil {
		return "", fmt.Errorf("renew the Bitbucket sign-in for %s: %w", cur.WorkspaceSlug, err)
	}
	if g.RefreshToken == "" {
		g.RefreshToken = held.RefreshToken
	}
	if g.RefreshToken != held.RefreshToken {
		if err := c.putRefresh(rctx, cur.OrgID, cur.ID, g.RefreshToken); err != nil {
			return "", err
		}
	}
	if !c.followRename(rctx, cur, g.AccessToken) {
		return "", c.needReconnect(ctx, cur, bitbucketAccountLeftMessage)
	}
	c.remember(cur, g)
	return g.AccessToken, nil
}

// followRename asks Bitbucket for the workspace by its UUID, which never
// changes, and saves a new slug or name if it has one. It costs one call
// per renewal. It reports false when the account that signed in is no
// longer a member of the workspace. Failing to ask doesn't fail the
// renewal.
func (c *BitbucketConnections) followRename(ctx context.Context, cur *BitbucketConnection, token string) (member bool) {
	ws, err := c.api.Workspace(ctx, token, cur.WorkspaceUUID)
	if errors.Is(err, ErrNotAMember) {
		return false
	}
	if err != nil {
		c.warnf("look up the Bitbucket workspace of "+cur.ID, err)
		return true
	}
	if !bitbucketSlug.MatchString(ws.Slug) || !strings.EqualFold(ws.UUID, cur.WorkspaceUUID) {
		return true
	}
	next := *cur
	next.WorkspaceSlug = ws.Slug
	if ws.Name != "" {
		next.WorkspaceName = ws.Name
	}
	if ws.AvatarURL != "" {
		next.AvatarURL = ws.AvatarURL
	}
	if next.WorkspaceSlug == cur.WorkspaceSlug && next.WorkspaceName == cur.WorkspaceName && next.AvatarURL == cur.AvatarURL {
		return true
	}
	next.UpdatedAt = c.now().UTC()
	// Saving moves the grant generation on, and the token is then cached
	// under the new one. Other processes renew once more than they need to.
	if err := c.store.SaveBitbucketWorkspace(ctx, &next); err != nil {
		c.warnf("save the renamed Bitbucket workspace "+cur.ID, err)
		return true
	}
	*cur = next
	return true
}

// needReconnect marks cur reconnect_needed with why, and drops its access
// token.
func (c *BitbucketConnections) needReconnect(ctx context.Context, cur *BitbucketConnection, why string) error {
	c.forget(cur.OrgID, cur.ID)
	if err := c.store.SetBitbucketWorkspaceStatus(ctx, cur.OrgID, cur.ID, BitbucketReconnectNeeded, why); err != nil {
		return err
	}
	cur.Status, cur.Message = BitbucketReconnectNeeded, why
	return nil
}

// putRefresh stores refresh as the connection's sign-in.
func (c *BitbucketConnections) putRefresh(ctx context.Context, orgID, id, refresh string) error {
	raw, err := json.Marshal(storedGrant{RefreshToken: refresh}) // #nosec G117 -- the refresh token is bound for Vault, never a response.
	if err != nil {
		return errors.New("encode the Bitbucket sign-in failed")
	}
	if err := c.secrets.Put(ctx, orgID, id, raw); err != nil {
		return fmt.Errorf("store the Bitbucket sign-in: %w", err)
	}
	return nil
}

// cached returns row's access token while it has more than a minute left
// and came from the sign-in row holds now. Any other entry is dropped.
func (c *BitbucketConnections) cached(row *BitbucketConnection) (string, bool) {
	key := row.OrgID + "/" + row.ID
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tokens[key]
	if !ok {
		return "", false
	}
	if t.generation != row.GrantGeneration || !c.now().Before(t.expires.Add(-bitbucketRenewAhead)) {
		delete(c.tokens, key)
		return "", false
	}
	return t.token, true
}

// remember keeps g's access token in memory under row's grant generation,
// unless it's already too close to expiry to hand out again.
func (c *BitbucketConnections) remember(row *BitbucketConnection, g BitbucketGrant) {
	if !c.now().Before(g.Expires.Add(-bitbucketRenewAhead)) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[row.OrgID+"/"+row.ID] = cachedToken{token: g.AccessToken, expires: g.Expires, generation: row.GrantGeneration}
}

// forget drops the connection's access token from memory.
func (c *BitbucketConnections) forget(orgID, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, orgID+"/"+id)
}

func (c *BitbucketConnections) warnf(msg string, err error) {
	if c.warn != nil {
		c.warn(msg, err)
	}
}

func bitbucketView(r *BitbucketConnection) BitbucketWorkspaceView {
	return BitbucketWorkspaceView{
		ID: r.ID, Workspace: r.WorkspaceSlug, Name: r.WorkspaceName, AvatarURL: r.AvatarURL,
		ManageURL:   "https://" + HostBitbucket + "/" + url.PathEscape(r.WorkspaceSlug) + "/workspace/settings",
		ConnectedBy: r.BitbucketUserName, Status: r.Status, Message: r.Message, CreatedAt: r.CreatedAt.UTC(),
	}
}

// bitbucketGate queues renewals in the process before they reach the
// database, as cloud sign-ins' renewGate does. A caller waiting on the
// database lock holds a pooled connection, so without it many callers of
// one workspace could take the whole pool and leave none for the caller
// holding the lock.
type bitbucketGate struct {
	mu    sync.Mutex
	queue map[string]*bitbucketQueue
	slots chan struct{}
}

// bitbucketQueue lets one caller of a connection through at a time.
type bitbucketQueue struct {
	turn  chan struct{}
	users int
}

func newBitbucketGate(slots int) *bitbucketGate {
	return &bitbucketGate{queue: map[string]*bitbucketQueue{}, slots: make(chan struct{}, slots)}
}

// enter waits for key's turn and then for a free slot, or for ctx to end.
// The caller calls leave when done.
func (g *bitbucketGate) enter(ctx context.Context, key string) (leave func(), err error) {
	g.mu.Lock()
	q := g.queue[key]
	if q == nil {
		q = &bitbucketQueue{turn: make(chan struct{}, 1)}
		g.queue[key] = q
	}
	q.users++
	g.mu.Unlock()
	forget := func() {
		g.mu.Lock()
		q.users--
		if q.users == 0 {
			delete(g.queue, key)
		}
		g.mu.Unlock()
	}
	select {
	case q.turn <- struct{}{}:
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		<-q.turn
		forget()
		return nil, ctx.Err()
	}
	return func() {
		<-g.slots
		<-q.turn
		forget()
	}, nil
}

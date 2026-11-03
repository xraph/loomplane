package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// BitbucketWebhookKeyLabel derives the key each Bitbucket webhook's secret
// is made from. The secrets themselves are never stored.
const BitbucketWebhookKeyLabel = "loomplane/bitbucket webhook v1"

// hookFinishTimeout bounds what Ensure does once Bitbucket has added a
// hook: recording it, or taking it off again. That runs even when the
// caller has gone, so a hook never outlives the row that knows of it.
const hookFinishTimeout = 15 * time.Second

// BitbucketPushActor is who an analysis a Bitbucket push started is
// recorded as created by.
const BitbucketPushActor = "bitbucket-push"

// Why push re-analysis is off. Each is shown beside the source.
const (
	reasonHTTPS     = "push re-analysis needs an https public_url, so Bitbucket can reach this Loomplane"
	reasonNoConn    = "connect the Bitbucket workspace this repository is in, then turn push re-analysis on"
	reasonReconnect = "the Bitbucket workspace needs connecting again before Loomplane can add a webhook"
	reasonTryAgain  = "Loomplane couldn't add the webhook on Bitbucket; try turning push re-analysis on again"
	reasonAdding    = "Loomplane is adding the webhook"
	reasonNotSetUp  = "push re-analysis isn't set up for this repository yet; turn it on to add a webhook"
	reasonUnknown   = "Loomplane couldn't read whether push re-analysis is on; try again shortly"
)

// PushState says whether a push to a source's repository re-analyzes it,
// and if not, why.
type PushState struct {
	On     bool   `json:"on"`
	Reason string `json:"reason,omitempty"`
}

// SourceHooks are told when a source is attached or removed, and asked
// whether pushes re-analyze one. Neither Added nor Removed can fail the
// change: it's already saved. Push returns nil for a source whose host has
// no say. Warn reports what the Service couldn't do for them.
type SourceHooks struct {
	Added   func(ctx context.Context, orgID string, src *Source)
	Removed func(ctx context.Context, orgID string, src *Source)
	Push    func(ctx context.Context, orgID string, src *Source) *PushState
	Warn    func(msg string, err error)
}

// Reap implements projects.Reaper. A project's sources go with it in the
// database's cascade, where Removed never hears of them, so they're read
// before the deletion and Removed runs for each once it's done. An
// environment's deletion leaves the sources alone.
func (s *Service) Reap(ctx context.Context, orgID, projectID, environmentID string) func(context.Context) {
	if environmentID != "" || s.hooks.Removed == nil {
		return nil
	}
	list, err := s.store.ListSources(ctx, orgID, projectID, false)
	if err != nil {
		if s.hooks.Warn != nil {
			s.hooks.Warn("list the sources of project "+projectID+" before deleting it", err)
		}
		return nil
	}
	if len(list) == 0 {
		return nil
	}
	return func(ctx context.Context) {
		seen := map[string]bool{}
		for _, src := range list {
			k := strings.ToLower(src.Repository)
			if !seen[k] {
				seen[k] = true
				s.hooks.Removed(ctx, orgID, src)
			}
		}
	}
}

// SetSourceHooks sets what hears about sources being attached and removed.
// Call it while the app registers.
func (s *Service) SetSourceHooks(h SourceHooks) { s.hooks = h }

// HookStore keeps the push webhooks.
type HookStore = repositories.HookStore

// HookConnections finds an organization's connection to a workspace, and a
// token to act in it with.
type HookConnections interface {
	ConnectionFor(ctx context.Context, orgID, workspace string) (*repositories.BitbucketConnection, string, error)
}

// HookAPI adds and removes webhooks on Bitbucket.
type HookAPI interface {
	CreateHook(ctx context.Context, token string, repo repositories.Repository, url, secret string) (string, error)
	DeleteHook(ctx context.Context, token string, repo repositories.Repository, uuid string) error
}

// HookAuditor records webhooks being added and removed: the repository and
// Loomplane's hook ID, never the secret. Recording must not fail the change.
type HookAuditor interface {
	RecordHook(ctx context.Context, action, orgID, repository, hookID string)
}

// BitbucketWebhooks keeps one push webhook per organization and Bitbucket
// repository, and re-analyzes the organization's sources when a signed
// push arrives. An OAuth consumer has no app-wide webhook the way a GitHub
// App does, so each repository gets its own.
type BitbucketWebhooks struct {
	// Key is derived with BitbucketWebhookKeyLabel. A hook's secret is the
	// HMAC of its ID under it.
	Key []byte
	// PublicURL is where Bitbucket delivers to. It has to be https.
	PublicURL string
	Hooks     HookStore
	Conns     HookConnections
	API       HookAPI
	// Audit may be nil.
	Audit HookAuditor
	// Authz decides who may turn push re-analysis on.
	Authz Authorizer
	// Sources lists the live sources in any organization that build from a
	// repository key, whatever its case. Every use narrows it to one
	// organization.
	Sources func(ctx context.Context, repository string) ([]*Source, error)
	// Start queues an analysis of a source's tracked branch.
	Start func(ctx context.Context, src *Source) (*Analysis, error)
	// DefaultBranch reads a repository's main branch from Bitbucket.
	DefaultBranch func(ctx context.Context, repo repositories.Repository, token string) (string, error)
	// Warn reports failures that don't fail the call. No error it's given
	// holds a token or a secret.
	Warn func(msg string, err error)

	now func() time.Time
}

// bitbucketPush is the part of Bitbucket's repo:push payload Deliver reads.
// A deleted branch's change has new set to null.
type bitbucketPush struct {
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Push struct {
		Changes []struct {
			New *struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"new"`
		} `json:"changes"`
	} `json:"push"`
}

// SourceHooks returns the hooks a Service calls for this.
func (w *BitbucketWebhooks) SourceHooks() SourceHooks {
	return SourceHooks{Added: w.added, Removed: w.removed, Push: w.State, Warn: w.warn}
}

// Secret is the secret Bitbucket signs a hook's deliveries with: the hex
// HMAC-SHA256 of hookID under Key.
func (w *BitbucketWebhooks) Secret(hookID string) string {
	m := hmac.New(sha256.New, w.Key)
	m.Write([]byte(hookID))
	return hex.EncodeToString(m.Sum(nil))
}

// deliveryURL is where Bitbucket posts a hook's pushes.
func (w *BitbucketWebhooks) deliveryURL(hookID string) string {
	return strings.TrimRight(w.PublicURL, "/") + "/api/v1/bitbucket/webhook/" + hookID
}

// Verify checks a delivery's X-Hub-Signature header, "sha256=<hex>",
// against its body and the hook's secret, in constant time.
func (w *BitbucketWebhooks) Verify(hookID, header string, body []byte) error {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok || len(w.Key) == 0 {
		return ErrBadSignature
	}
	sum, err := hex.DecodeString(got)
	if err != nil {
		return ErrBadSignature
	}
	want := hmac.New(sha256.New, []byte(w.Secret(hookID)))
	want.Write(body)
	if !hmac.Equal(sum, want.Sum(nil)) {
		return ErrBadSignature
	}
	return nil
}

// Ensure makes sure the organization has a push webhook on repo, adding
// one if it can, and says whether pushes re-analyze it now. A hook that
// can't be added is kept off, with the reason, and the same row is turned
// on by a later try.
func (w *BitbucketWebhooks) Ensure(ctx context.Context, orgID string, repo repositories.Repository) PushState {
	key := repo.Key()
	row, err := w.Hooks.BitbucketHookFor(ctx, orgID, key)
	switch {
	case err == nil && row.Status == repositories.HookOn:
		return PushState{On: true}
	case errors.Is(err, repositories.ErrHookNotFound):
		row = nil
	case err != nil:
		w.warn("read the Bitbucket webhook of "+key, err)
		return PushState{Reason: reasonUnknown}
	}
	if !httpsURL(w.PublicURL) {
		return w.off(ctx, orgID, key, row, "", reasonHTTPS)
	}
	conn, token, err := w.Conns.ConnectionFor(ctx, orgID, repo.Owner)
	switch {
	case errors.Is(err, repositories.ErrWorkspaceNotFound), errors.Is(err, repositories.ErrBitbucketNotConfigured):
		return w.off(ctx, orgID, key, row, "", reasonNoConn)
	case err != nil:
		w.warn("find the Bitbucket connection for "+key, err)
		return w.off(ctx, orgID, key, row, "", reasonTryAgain)
	case token == "":
		return w.off(ctx, orgID, key, row, conn.ID, reasonReconnect)
	}
	if row == nil {
		// The row goes in first, off, so two attaches at once add one hook:
		// the second finds the first's row and leaves it be.
		if row, err = w.reserve(ctx, orgID, key, conn.ID); row == nil {
			if err != nil {
				w.warn("save the Bitbucket webhook of "+key, err)
				return PushState{Reason: reasonTryAgain}
			}
			return w.State(ctx, orgID, &Source{Repository: key}).orOff()
		}
	}
	if w.API == nil {
		return w.off(ctx, orgID, key, row, conn.ID, reasonNoConn)
	}
	uuid, err := w.API.CreateHook(ctx, token, repo, w.deliveryURL(row.HookID), w.Secret(row.HookID))
	if err != nil {
		reason := reasonTryAgain
		if errors.Is(err, repositories.ErrHookNotAdmin) {
			reason = repositories.ErrHookNotAdmin.Error()
		} else {
			w.warn("add a Bitbucket webhook to "+key, err)
		}
		return w.off(ctx, orgID, key, row, conn.ID, reason)
	}
	// Bitbucket has the hook now, so a caller giving up mustn't leave it
	// there with no row that knows of it.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookFinishTimeout)
	defer cancel()
	row.ConnectionID, row.BitbucketHookUUID, row.Status, row.Reason, row.UpdatedAt = conn.ID, uuid, repositories.HookOn, "", w.clock()
	won, err := w.Hooks.UpdateBitbucketHookIfOff(fctx, row)
	if err != nil || !won {
		// Either the row can't record it, or another try turned it on
		// first. This hook is one too many either way.
		if err != nil {
			w.warn("turn the Bitbucket webhook of "+key+" on", err)
		}
		if derr := w.API.DeleteHook(fctx, token, repo, uuid); derr != nil {
			w.warn("remove the Bitbucket webhook just added to "+key, derr)
		}
		if err != nil {
			return PushState{Reason: reasonTryAgain}
		}
		return w.State(fctx, orgID, &Source{Repository: key}).orOff()
	}
	if w.Audit != nil {
		w.Audit.RecordHook(fctx, "bitbucket.webhook.add", orgID, key, row.HookID)
	}
	return PushState{On: true}
}

// reserve saves a new off row for the repository. It returns nil, and no
// error, when another call saved one first.
func (w *BitbucketWebhooks) reserve(ctx context.Context, orgID, key, connectionID string) (*repositories.BitbucketHook, error) {
	id, err := typeid.Generate("bbhook")
	if err != nil {
		return nil, fmt.Errorf("generate webhook id: %w", err)
	}
	now := w.clock()
	row := &repositories.BitbucketHook{
		HookID: id.String(), OrgID: orgID, ConnectionID: connectionID, Repository: key,
		Status: repositories.HookOff, Reason: reasonAdding, CreatedAt: now, UpdatedAt: now,
	}
	switch err := w.Hooks.SaveBitbucketHook(ctx, row); {
	case errors.Is(err, repositories.ErrHookExists):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return row, nil
}

// off keeps the repository's hook off with reason, in the row it has or a
// new one, and returns that state.
func (w *BitbucketWebhooks) off(ctx context.Context, orgID, key string, row *repositories.BitbucketHook, connectionID, reason string) PushState {
	now := w.clock()
	if row == nil {
		id, err := typeid.Generate("bbhook")
		if err != nil {
			w.warn("generate a webhook id", err)
			return PushState{Reason: reason}
		}
		row = &repositories.BitbucketHook{
			HookID: id.String(), OrgID: orgID, ConnectionID: connectionID, Repository: key,
			Status: repositories.HookOff, Reason: reason, CreatedAt: now, UpdatedAt: now,
		}
		if err := w.Hooks.SaveBitbucketHook(ctx, row); err != nil && !errors.Is(err, repositories.ErrHookExists) {
			w.warn("save the Bitbucket webhook of "+key, err)
		}
		return PushState{Reason: reason}
	}
	// Only a row with no hook on Bitbucket is marked off: one another try
	// turned on meanwhile stays on.
	off := *row
	off.ConnectionID, off.BitbucketHookUUID, off.Status, off.Reason, off.UpdatedAt = connectionID, "", repositories.HookOff, reason, now
	switch updated, err := w.Hooks.UpdateBitbucketHookIfOff(ctx, &off); {
	case err != nil:
		w.warn("save the Bitbucket webhook of "+key, err)
	case !updated:
		return w.State(ctx, orgID, &Source{Repository: key}).orOff()
	}
	return PushState{Reason: reason}
}

// State says whether pushes re-analyze src, from its repository's hook in
// the organization. It's nil for a source that isn't on Bitbucket.
func (w *BitbucketWebhooks) State(ctx context.Context, orgID string, src *Source) *PushState {
	repo, ok := onBitbucket(src.Repository)
	if !ok {
		return nil
	}
	row, err := w.Hooks.BitbucketHookFor(ctx, orgID, repo.Key())
	switch {
	case errors.Is(err, repositories.ErrHookNotFound):
		return &PushState{Reason: reasonNotSetUp}
	case err != nil:
		w.warn("read the Bitbucket webhook of "+repo.Key(), err)
		return &PushState{Reason: reasonUnknown}
	case row.Status == repositories.HookOn:
		return &PushState{On: true}
	}
	return &PushState{Reason: row.Reason}
}

// orOff is the state, or off for unknown reasons when there's none.
func (s *PushState) orOff() PushState {
	if s == nil {
		return PushState{Reason: reasonUnknown}
	}
	return *s
}

// Enable is the Turn on action: it retries adding the webhook for a
// repository the caller's organization has a live source on.
func (w *BitbucketWebhooks) Enable(ctx context.Context, p security.Principal, repository string) (PushState, error) {
	if err := w.Authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return PushState{}, err
	}
	parsed, err := repositories.ParseURL(repository)
	if err != nil {
		return PushState{}, fmt.Errorf("%w: %q isn't a Bitbucket repository", ErrInvalidSource, repository)
	}
	// The same key attaching gives it, lowercase.
	repo, ok := onBitbucket(parsed.Key())
	if !ok {
		return PushState{}, fmt.Errorf("%w: %q isn't a Bitbucket repository", ErrInvalidSource, repository)
	}
	live, err := w.live(ctx, p.OrgID, repo.Key(), "")
	if err != nil {
		return PushState{}, err
	}
	if !live {
		return PushState{}, fmt.Errorf("%w: no project in this organization builds from %s", ErrSourceNotFound, repo.Key())
	}
	return w.Ensure(ctx, p.OrgID, repo), nil
}

// Release removes the organization's webhook on repo, from Bitbucket and
// then from here. A failure on Bitbucket's side is reported and the row
// goes anyway: a delivery to a missing row is a 404.
func (w *BitbucketWebhooks) Release(ctx context.Context, orgID string, repo repositories.Repository) {
	row, err := w.Hooks.BitbucketHookFor(ctx, orgID, repo.Key())
	if errors.Is(err, repositories.ErrHookNotFound) {
		return
	}
	if err != nil {
		w.warn("read the Bitbucket webhook of "+repo.Key(), err)
		return
	}
	w.drop(ctx, row, repo, w.tokenFor(ctx, orgID, repo.Owner, row))
}

// ReleaseAll removes every webhook a workspace connection added, as the
// workspace is disconnected. Each row goes even when Bitbucket refuses to
// remove its hook.
func (w *BitbucketWebhooks) ReleaseAll(ctx context.Context, orgID, connectionID string) {
	rows, err := w.Hooks.BitbucketHooksIn(ctx, orgID, connectionID)
	if err != nil {
		w.warn("list the webhooks of Bitbucket connection "+connectionID, err)
		return
	}
	tokens := map[string]string{}
	for _, row := range rows {
		repo, err := repositories.ParseKey(row.Repository)
		if err != nil {
			w.warn("read the repository of Bitbucket webhook "+row.HookID, err)
			w.dropRow(ctx, row)
			continue
		}
		owner := strings.ToLower(repo.Owner)
		token, ok := tokens[owner]
		if !ok {
			token = w.tokenFor(ctx, orgID, repo.Owner, row)
			tokens[owner] = token
		}
		w.drop(ctx, row, repo, token)
	}
}

// tokenFor is a token to remove row's hook with, or "" when it has none on
// Bitbucket or no connection can act.
func (w *BitbucketWebhooks) tokenFor(ctx context.Context, orgID, workspace string, row *repositories.BitbucketHook) string {
	if row.BitbucketHookUUID == "" {
		return ""
	}
	_, token, err := w.Conns.ConnectionFor(ctx, orgID, workspace)
	if err != nil {
		w.warn("find the Bitbucket connection to remove webhook "+row.HookID, err)
		return ""
	}
	return token
}

// drop removes row's hook from Bitbucket if it has one there, and then the
// row, whatever Bitbucket said.
func (w *BitbucketWebhooks) drop(ctx context.Context, row *repositories.BitbucketHook, repo repositories.Repository, token string) {
	if row.BitbucketHookUUID != "" {
		switch {
		case token == "" || w.API == nil:
			w.warn("remove Bitbucket webhook "+row.HookID+" from "+row.Repository, errors.New("no connection can act on the repository, so remove it on Bitbucket by hand"))
		default:
			if err := w.API.DeleteHook(ctx, token, repo, row.BitbucketHookUUID); err != nil {
				w.warn("remove Bitbucket webhook "+row.HookID+" from "+row.Repository, err)
			}
		}
	}
	w.dropRow(ctx, row)
}

func (w *BitbucketWebhooks) dropRow(ctx context.Context, row *repositories.BitbucketHook) {
	if err := w.Hooks.DeleteBitbucketHook(ctx, row.HookID); err != nil && !errors.Is(err, repositories.ErrHookNotFound) {
		w.warn("delete Bitbucket webhook "+row.HookID, err)
		return
	}
	if w.Audit != nil {
		w.Audit.RecordHook(ctx, "bitbucket.webhook.remove", row.OrgID, row.Repository, row.HookID)
	}
}

// Deliver acts on a delivery to hookID and returns how many analyses it
// started. A hook that's unknown or off is ErrHookNotFound, and a
// signature that doesn't match is ErrBadSignature. Only a push to the
// repository's default branch re-analyzes, and only the sources of the
// hook's own organization that track it.
func (w *BitbucketWebhooks) Deliver(ctx context.Context, hookID, event, signature string, body []byte) (int, error) {
	row, err := w.Hooks.GetBitbucketHook(ctx, hookID)
	if errors.Is(err, repositories.ErrHookNotFound) || (err == nil && row.Status != repositories.HookOn) {
		return 0, repositories.ErrHookNotFound
	}
	if err != nil {
		return 0, err
	}
	if verr := w.Verify(hookID, signature, body); verr != nil {
		return 0, verr
	}
	// A hook outliving every source of its organization on the repository
	// (a cleanup that failed, say) goes as soon as it's heard from.
	live, err := w.live(ctx, row.OrgID, row.Repository, "")
	if err != nil {
		return 0, err
	}
	if !live {
		if repo, ok := onBitbucket(row.Repository); ok {
			w.drop(ctx, row, repo, w.tokenFor(ctx, row.OrgID, repo.Owner, row))
		} else {
			w.dropRow(ctx, row)
		}
		return 0, nil
	}
	if event != "repo:push" {
		return 0, nil
	}
	var push bitbucketPush
	if jerr := json.Unmarshal(body, &push); jerr != nil {
		return 0, fmt.Errorf("%w: the push isn't JSON Loomplane can read", ErrInvalid)
	}
	repo, ok := onBitbucket(row.Repository)
	if !ok || !strings.EqualFold(repositories.HostBitbucket+"/"+push.Repository.FullName, row.Repository) {
		return 0, nil
	}
	branches := map[string]bool{}
	for _, c := range push.Push.Changes {
		if c.New != nil && c.New.Type == "branch" && c.New.Name != "" {
			branches[c.New.Name] = true
		}
	}
	if len(branches) == 0 {
		return 0, nil
	}
	// The payload doesn't say which branch is the default, so Bitbucket is
	// asked, once.
	_, token, err := w.Conns.ConnectionFor(ctx, row.OrgID, repo.Owner)
	if err != nil && !errors.Is(err, repositories.ErrWorkspaceNotFound) && !errors.Is(err, repositories.ErrBitbucketNotConfigured) {
		return 0, err
	}
	def, err := w.DefaultBranch(ctx, repo, token)
	if err != nil {
		return 0, fmt.Errorf("read the default branch of %s: %w", row.Repository, err)
	}
	if !branches[def] {
		return 0, nil
	}
	candidates, err := w.Sources(ctx, row.Repository)
	if err != nil {
		return 0, err
	}
	allow := func(orgID string) bool { return orgID == row.OrgID }
	return startTracking(ctx, candidates, row.Repository, def, allow, w.Start)
}

// added adds the hook for a newly attached Bitbucket source.
func (w *BitbucketWebhooks) added(ctx context.Context, orgID string, src *Source) {
	if repo, ok := onBitbucket(src.Repository); ok {
		w.Ensure(ctx, orgID, repo)
	}
}

// removed releases the hook once the organization's last live source on
// the repository is gone.
func (w *BitbucketWebhooks) removed(ctx context.Context, orgID string, src *Source) {
	repo, ok := onBitbucket(src.Repository)
	if !ok {
		return
	}
	live, err := w.live(ctx, orgID, src.Repository, src.ID)
	if err != nil {
		w.warn("list the sources on "+src.Repository, err)
		return
	}
	if !live {
		w.Release(ctx, orgID, repo)
	}
}

// live reports whether the organization has a live source on repository,
// other than the one with ID except.
func (w *BitbucketWebhooks) live(ctx context.Context, orgID, repository, except string) (bool, error) {
	list, err := w.Sources(ctx, repository)
	if err != nil {
		return false, err
	}
	for _, s := range list {
		if s.OrgID == orgID && s.ID != except && s.RemovedAt == nil && strings.EqualFold(s.Repository, repository) {
			return true, nil
		}
	}
	return false, nil
}

func (w *BitbucketWebhooks) warn(msg string, err error) {
	if w.Warn != nil {
		w.Warn(msg, err)
	}
}

func (w *BitbucketWebhooks) clock() time.Time {
	if w.now != nil {
		return w.now().UTC()
	}
	return time.Now().UTC()
}

// onBitbucket reads a stored key as a Bitbucket repository.
func onBitbucket(key string) (repositories.Repository, bool) {
	repo, err := repositories.ParseKey(key)
	if err != nil || !strings.EqualFold(repo.Host, repositories.HostBitbucket) {
		return repositories.Repository{}, false
	}
	// Bitbucket's slugs are lowercase, whatever case a source was added in.
	repo.Host, repo.Owner, repo.Name = repositories.HostBitbucket, strings.ToLower(repo.Owner), strings.ToLower(repo.Name)
	return repo, true
}

// httpsURL reports whether raw is an absolute https address.
func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

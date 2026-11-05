package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/principal"
	"github.com/xraph/grove/drivers/pgdriver"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/approvaladmission"
	"github.com/xraph/loomplane/internal/security"
)

type approvalSourceKey struct{}

// approvalSource contains copied nonsecret facts from the successful guard.
// Its zero value records an unsupported source without changing normal traffic.
type approvalSource struct {
	actor               security.Principal
	app, class, session string
	created, expires    time.Time
}

type approvalAdmitter struct {
	key      []byte
	app      string
	now      func() time.Time
	keyFacts func(context.Context, string) (*key.Key, bool, error)
}

func newApprovalAdmitter(secret, app string, facts func(context.Context, string) (*key.Key, bool, error)) (*approvalAdmitter, *approvaladmission.Verifier, error) {
	material, err := security.DeriveKey(secret, approvaladmission.KeyPurpose)
	if err != nil {
		return nil, nil, err
	}
	verifier, err := approvaladmission.NewVerifier(material)
	if err != nil {
		return nil, nil, err
	}
	return &approvalAdmitter{key: material, app: app, now: time.Now, keyFacts: facts}, verifier, nil
}

func sameOrdinary(a, b security.Principal) bool {
	return a.Kind == security.KindUser && b.Kind == security.KindUser && a.OnBehalfOf == nil && b.OnBehalfOf == nil && a.Fleet == nil && b.Fleet == nil && !a.ReadOnly && !b.ReadOnly && a.UserID == b.UserID && a.OrgID == b.OrgID && a.KeyID == b.KeyID
}

func (e *Extension) captureSessionApproval(ctx context.Context, p security.Principal) context.Context {
	source := approvalSource{}
	if s, ok := e.sessionApprovalSource(ctx, p); ok {
		source = s
	}
	return context.WithValue(ctx, approvalSourceKey{}, source)
}

func (e *Extension) sessionApprovalSource(ctx context.Context, p security.Principal) (approvalSource, bool) {
	method, ok := authmw.AuthMethodFrom(ctx)
	if !ok || method != "session" || p.KeyID != "" || !sameOrdinary(p, p) {
		return approvalSource{}, false
	}
	s, ok := authmw.SessionFrom(ctx)
	if !ok || s == nil {
		return approvalSource{}, false
	}
	u, ok := authmw.UserFrom(ctx)
	if !ok || u == nil {
		return approvalSource{}, false
	}
	app, aok := authmw.AppIDFrom(ctx)
	org, ook := authmw.OrgIDFrom(ctx)
	uid, uok := authmw.UserIDFrom(ctx)
	sid, sok := authmw.SessionIDFrom(ctx)
	if !aok || !ook || !uok || !sok || app.String() != e.cfg.Identity.AppID || s.AppID != app || u.AppID != app || s.UserID != uid || u.ID != uid || uid.String() != p.UserID || s.OrgID != org || org.String() != p.OrgID || s.ID != sid || sid.IsNil() || s.CreatedAt.IsZero() || s.ExpiresAt.IsZero() || s.EnvID != u.EnvID {
		return approvalSource{}, false
	}
	if !s.IsHumanPrincipal() || !s.ServiceAccountID.IsNil() || !s.AgentID.IsNil() || !s.GrantID.IsNil() || !s.DelegationID.IsNil() || len(s.Actors) != 0 || s.ActorGrant != "" || s.ClientID != "" || s.DPoPJKT != "" || len(s.Scopes) != 0 || len(s.Audience) != 0 || u.DeletedAt != nil {
		return approvalSource{}, false
	}
	if env, ok := authmw.EnvIDFrom(ctx); ok && env != s.EnvID {
		return approvalSource{}, false
	}
	if _, ok := authmw.ImpersonatorFrom(ctx); ok {
		return approvalSource{}, false
	}
	if actors, ok := authmw.ActorsFrom(ctx); ok && len(actors) != 0 {
		return approvalSource{}, false
	}
	if _, ok := authmw.PendingOAuthSessionFrom(ctx); ok {
		return approvalSource{}, false
	}
	if ref, ok := authmw.PrincipalRefFrom(ctx); ok && ref != (principal.Ref{Kind: principal.KindUser, ID: p.UserID}) {
		return approvalSource{}, false
	}
	if full, ok := authmw.PrincipalFrom(ctx); ok {
		if full == nil || full.Ref != (principal.Ref{Kind: principal.KindUser, ID: p.UserID}) || full.AppID != app || (!full.OrgID.IsNil() && full.OrgID != org) || full.EnvID != u.EnvID || full.Owner != nil || full.Parent != nil || full.ExpiresAt != nil || full.Disabled || len(full.Scopes) != 0 {
			return approvalSource{}, false
		}
	}
	if prior, ok := security.PrincipalFrom(ctx); ok && !sameOrdinary(prior, p) {
		return approvalSource{}, false
	}
	// The middleware checked bans and verification. Keep only the ordinary
	// source's admission interval; later acceptance never reloads this session.
	return approvalSource{actor: p, app: app.String(), class: approvaladmission.SessionSource, session: s.ID.String(), created: s.CreatedAt, expires: s.ExpiresAt}, true
}

func (e *Extension) captureKeyApproval(ctx context.Context, p security.Principal) context.Context {
	source := approvalSource{}
	_, sessionPresent := authmw.SessionFrom(ctx)
	_, methodPresent := authmw.AuthMethodFrom(ctx)
	_, userPresent := authmw.UserIDFrom(ctx)
	_, loadedUser := authmw.UserFrom(ctx)
	_, principalPresent := authmw.PrincipalRefFrom(ctx)
	_, fullPrincipalPresent := authmw.PrincipalFrom(ctx)
	app, appPresent := authmw.AppIDFrom(ctx)
	org, orgPresent := authmw.OrgIDFrom(ctx)
	_, sessionIDPresent := authmw.SessionIDFrom(ctx)
	_, impersonatorPresent := authmw.ImpersonatorFrom(ctx)
	_, pendingOAuth := authmw.PendingOAuthSessionFrom(ctx)
	actors, _ := authmw.ActorsFrom(ctx)
	prior, priorPresent := security.PrincipalFrom(ctx)
	if !sessionPresent && !methodPresent && !userPresent && !loadedUser && !principalPresent && !fullPrincipalPresent && (!appPresent || app.String() == e.cfg.Identity.AppID) && (!orgPresent || org.String() == p.OrgID) && !sessionIDPresent && !impersonatorPresent && !pendingOAuth && len(actors) == 0 && (!priorPresent || sameOrdinary(prior, p)) && p.KeyID != "" && sameOrdinary(p, p) {
		source = approvalSource{actor: p, app: e.cfg.Identity.AppID, class: approvaladmission.KeySource}
	}
	return context.WithValue(ctx, approvalSourceKey{}, source)
}

func (a *approvalAdmitter) AdmitPlanApproval(ctx context.Context, r approvaladmission.Request) (approvaladmission.Envelope, error) {
	source, ok := ctx.Value(approvalSourceKey{}).(approvalSource)
	current, present := security.PrincipalFrom(ctx)
	if a == nil || !ok || !present || source.app != a.app || !sameOrdinary(source.actor, r.Actor) || !sameOrdinary(current, r.Actor) || source.actor.OrgID != r.OrgID {
		return "", approvaladmission.ErrInvalid
	}
	at := a.now().UTC()
	switch source.class {
	case approvaladmission.SessionSource:
		if source.created.After(at) || !source.expires.After(at) || source.actor.KeyID != "" {
			return "", approvaladmission.ErrInvalid
		}
	case approvaladmission.KeySource:
		if a.keyFacts == nil {
			return "", approvaladmission.ErrInvalid
		}
		k, scoped, err := a.keyFacts(ctx, source.actor.KeyID)
		at = a.now().UTC()
		if err != nil || scoped || apikeys.ValidateSavedKeyRecordAt(source.actor, a.app, k, at) != nil {
			return "", approvaladmission.ErrInvalid
		}
	default:
		return "", approvaladmission.ErrInvalid
	}
	c := approvaladmission.Claims{Version: 1, Operation: approvaladmission.Operation, AppID: a.app, Scope: r.Scope, RequestID: r.RequestID, GroupSetDigest: r.GroupSetDigest, Note: r.Note, Source: source.class, UserID: source.actor.UserID, KeyID: source.actor.KeyID, SessionID: source.session, AdmittedAt: at, AcceptBefore: r.AcceptBefore.UTC()}
	payload, err := approvaladmission.Payload(c)
	if err != nil {
		return "", err
	}
	prefix := "v1." + approvaladmission.KeyID(a.key) + "." + base64.RawURLEncoding.EncodeToString(payload)
	h := hmac.New(sha256.New, a.key)
	_, _ = h.Write([]byte(approvaladmission.KeyPurpose + "\x00" + prefix))
	envelope := approvaladmission.Envelope(prefix + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)))
	if len(envelope) > approvaladmission.MaxEnvelopeBytes {
		return "", approvaladmission.ErrInvalid
	}
	return envelope, nil
}

// approvalKeyReader uses the configured main pool. Its schema is captured only
// after ordinary startup, and every read checks the installed store's resolution.
type approvalKeyReader struct {
	db     *pgdriver.PgDB
	schema atomic.Pointer[string]
}

func (r *approvalKeyReader) prepare(ctx context.Context) {
	r.schema.Store(nil)
	if r.db == nil {
		return
	}
	var schema string
	if err := r.db.QueryRow(ctx, `SELECT pg_catalog.current_schema()::pg_catalog.text`).Scan(&schema); err != nil || schema == "" {
		return
	}
	if err := r.db.QueryRow(ctx, `SELECT 1 WHERE `+approvalSchemaGuard(), schema, pgx.Identifier{schema, "keysmith_keys"}.Sanitize(), pgx.Identifier{schema, "keysmith_key_scopes"}.Sanitize()).Scan(new(int)); err != nil {
		return
	}
	r.schema.Store(&schema)
}
func approvalSchemaGuard() string {
	return `pg_catalog.current_schema()::pg_catalog.text OPERATOR(pg_catalog.=) $1::pg_catalog.text
 AND pg_catalog.to_regclass('keysmith_keys') OPERATOR(pg_catalog.=) pg_catalog.to_regclass($2)
 AND pg_catalog.to_regclass('keysmith_key_scopes') OPERATOR(pg_catalog.=) pg_catalog.to_regclass($3)`
}
func (r *approvalKeyReader) read(ctx context.Context, kid string) (*key.Key, bool, error) {
	schema := r.schema.Load()
	if r.db == nil || schema == nil {
		return nil, false, approvaladmission.ErrInvalid
	}
	keys, scopes := pgx.Identifier{*schema, "keysmith_keys"}.Sanitize(), pgx.Identifier{*schema, "keysmith_key_scopes"}.Sanitize()
	var raw []byte
	q := `SELECT pg_catalog.jsonb_build_object('id',k.id,'app_id',k.app_id,'tenant_id',k.tenant_id,'created_by',k.created_by,'state',k.state,'metadata',k.metadata,'expires_at',k.expires_at,'revoked_at',k.revoked_at,'policy_id',k.policy_id,
 'has_scopes',EXISTS(SELECT 1 FROM ` + scopes + ` ks WHERE ks.key_id OPERATOR(pg_catalog.=) k.id))
 FROM ` + keys + ` k WHERE ` + approvalSchemaGuard() + ` AND k.id OPERATOR(pg_catalog.=) $4::pg_catalog.text`
	if err := r.db.QueryRow(ctx, q, *schema, keys, scopes, kid).Scan(&raw); err != nil {
		return nil, false, approvaladmission.ErrInvalid
	}
	var row struct {
		key.Key
		HasScopes bool `json:"has_scopes"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, false, approvaladmission.ErrInvalid
	}
	parsed, err := keyid.ParseKeyID(kid)
	if err != nil || row.ID != parsed {
		return nil, false, approvaladmission.ErrInvalid
	}
	return &row.Key, row.HasScopes, nil
}

var _ approvaladmission.Admitter = (*approvalAdmitter)(nil)

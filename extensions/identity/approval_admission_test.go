package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/authsome"
	"github.com/xraph/authsome/bridge"
	authid "github.com/xraph/authsome/id"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/principal"
	"github.com/xraph/authsome/session"
	authmem "github.com/xraph/authsome/store/memory"
	"github.com/xraph/authsome/user"
	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/durable"
	dispatchengine "github.com/xraph/dispatch/engine"
	dispatchmem "github.com/xraph/dispatch/store/memory"
	"github.com/xraph/forge"
	"github.com/xraph/keysmith"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	keymem "github.com/xraph/keysmith/store/memory"
	"github.com/xraph/warden"
	wardenmem "github.com/xraph/warden/store/memory"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/approvaladmission"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type admissionStore struct {
	planning.Store
	plan *planning.Plan
}

func (s *admissionStore) GetPlan(context.Context, string, string) (*planning.Plan, error) {
	return s.plan, nil
}

type admissionWorkflow struct {
	durable  *orchestration.Deployments
	history  *dispatchmem.Store
	decision orchestration.Decision
	request  string
	plan     *planning.Plan
	calls    int
}

func (w *admissionWorkflow) Start(context.Context, string, string, time.Duration) error { return nil }
func (w *admissionWorkflow) Cancel(context.Context, string, string) error               { return nil }
func (w *admissionWorkflow) Decide(ctx context.Context, planID string, request string, d orchestration.Decision) error {
	if err := w.durable.Decide(ctx, planID, request, d); err != nil {
		return err
	}
	w.calls++
	w.decision = d
	w.request = request
	w.plan.Status = planning.StatusApproved
	return nil
}

type admissionAllow struct{}

func (admissionAllow) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}
func (admissionAllow) Allowed(context.Context, security.Principal, security.Permission) (bool, error) {
	return true, nil
}

type admissionFixture struct {
	e        *Extension
	session  *session.Session
	user     *user.User
	store    *authmem.Store
	keys     *keymem.Store
	signer   *approvalAdmitter
	verifier *approvaladmission.Verifier
	plan     *planning.Plan
	workflow *admissionWorkflow
	service  *planning.Service
}

func newAdmissionFixture(t *testing.T) *admissionFixture {
	t.Helper()
	now := time.Now().UTC()
	u := &user.User{ID: authid.NewUserID(), AppID: authid.NewAppID(), Email: "reviewer@example.test", EmailVerified: true, CreatedAt: now.Add(-time.Hour)}
	sess := &session.Session{ID: authid.NewSessionID(), AppID: u.AppID, UserID: u.ID, OrgID: authid.NewOrgID(), Token: "opaque-admission-test-token", RefreshToken: "opaque-admission-refresh", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ExpiresAt: now.Add(time.Hour), RefreshTokenExpiresAt: now.Add(24 * time.Hour)}
	st := authmem.New()
	if err := st.CreateUser(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	w, err := warden.NewEngine(warden.WithStore(wardenmem.New()))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := authsome.NewEngine(authsome.WithStore(st), authsome.WithWarden(w), authsome.WithChronicle(bridge.NewMemoryChronicle()))
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })
	cfg := config.Defaults()
	cfg.Identity.AppID = u.AppID.String()
	e := New(cfg)
	e.SetLogger(forge.NewNoopLogger())
	e.engine = engine
	km := keymem.New()
	e.smith, err = keysmith.NewEngine(keysmith.WithStore(km))
	if err != nil {
		t.Fatal(err)
	}
	e.keys = apikeys.New(e.smith, cfg.Identity.AppID, admissionAllow{}, nil)
	loader := func(ctx context.Context, kid string) (*key.Key, bool, error) {
		id, parseErr := keyid.ParseKeyID(kid)
		if parseErr != nil {
			return nil, false, parseErr
		}
		k, getErr := km.Keys().Get(ctx, id)
		if getErr != nil {
			return nil, false, getErr
		}
		scopes, listErr := km.Scopes().ListByKey(ctx, id)
		return k, len(scopes) > 0, listErr
	}
	signer, verifier, err := newApprovalAdmitter(cfg.SecretKey, cfg.Identity.AppID, loader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{OrgID: sess.OrgID.String(), ProjectID: typeid.MustGenerate("proj").String(), EnvironmentID: typeid.MustGenerate("env").String()}
	deadline := now.Add(time.Hour)
	plan := &planning.Plan{ID: typeid.MustGenerate("plan").String(), OrgID: scope.OrgID, ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID, Status: planning.StatusReady, Revision: strings.Repeat("a", 64), ExpiresAt: &deadline, PlacementGroups: []domain.QualifiedGroupPlan{{Scope: scope, GroupID: typeid.MustGenerate("pgroup").String(), Generation: 1, Revision: strings.Repeat("b", 64), ConfigRevision: strings.Repeat("c", 64)}}}
	history := dispatchmem.New()
	dispatcher, err := dispatch.New(dispatch.WithStore(history))
	if err != nil {
		t.Fatal(err)
	}
	durableWorkflow := orchestration.NewDeployments()
	durableEngine, err := dispatchengine.Build(dispatcher, dispatchengine.WithDurableWorkflows(durableWorkflow.Options("source-admission", 1)))
	if err != nil {
		t.Fatal(err)
	}
	workflow := &admissionWorkflow{plan: plan, durable: durableWorkflow, history: history}
	svc := planning.NewService(planning.Deps{Store: &admissionStore{plan: plan}, Authz: admissionAllow{}, Workflow: workflow, ApprovalAdmitter: signer, ApprovalVerifier: verifier, ApprovalAppID: cfg.Identity.AppID})
	durableWorkflow.Bind(svc, nil, func() *dispatchengine.Engine { return durableEngine })
	if err = durableWorkflow.Start(t.Context(), plan.OrgID, plan.ID, time.Hour); err != nil {
		t.Fatal(err)
	}

	return &admissionFixture{e: e, session: sess, user: u, store: st, keys: km, signer: signer, verifier: verifier, plan: plan, workflow: workflow, service: svc}
}
func (f *admissionFixture) request(t *testing.T, token, header string, after forge.Middleware) *httptest.ResponseRecorder {
	t.Helper()
	f.plan.Status = planning.StatusReady
	router := forge.NewRouter()
	handler := func(c forge.Context) error {
		p, _ := security.PrincipalFrom(c.Context())
		_, err := f.service.Approve(c.Context(), p, f.plan.ID, planning.DecideInput{Revision: f.plan.Revision, Note: " reviewed "})
		if err != nil {
			return forge.NewHTTPError(http.StatusForbidden, err.Error())
		}
		return c.NoContent(http.StatusNoContent)
	}
	if after != nil {
		handler = after(handler)
	}
	if err := router.POST("/approve", f.e.guard()(handler)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/approve", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if header != "" {
		req.Header.Set(security.HeaderOrganization, header)
	}
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}
func (f *admissionFixture) verify(t *testing.T) approvaladmission.Verified {
	t.Helper()
	d := f.workflow.decision
	history, err := f.workflow.history.ReadHistory(t.Context(), durable.Key{Namespace: orchestration.DurableNamespace, WorkflowID: f.plan.ID, RunID: "1"}, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range history {
		if event.Type != durable.EventSignalReceived {
			continue
		}
		var signal durable.Signal
		if err = json.Unmarshal(event.Payload, &signal); err != nil {
			t.Fatal(err)
		}
		var delivered orchestration.Decision
		if err = json.Unmarshal(signal.Input, &delivered); err != nil {
			t.Fatal(err)
		}
		if signal.ID != f.workflow.request || signal.Name != orchestration.SignalDecision || !reflect.DeepEqual(delivered, d) {
			t.Fatal("authenticated source changed before durable signal")
		}
		found = true
	}
	if !found {
		t.Fatal("authenticated approval never reached durable signal history")
	}
	if d.Actor == nil || d.RequestID == "" || d.RequestID != f.workflow.request || d.GroupAdmission == "" {
		t.Fatalf("missing exact signal binding: %#v", d)
	}
	scope := approvaladmission.Scope{OrgID: f.plan.OrgID, ProjectID: f.plan.ProjectID, EnvironmentID: f.plan.EnvironmentID, PlanID: f.plan.ID, Revision: f.plan.Revision}
	g := f.plan.PlacementGroups[0]
	digest, err := approvaladmission.GroupSetDigest(scope, []approvaladmission.GroupPin{{ID: g.GroupID, Generation: g.Generation, Revision: g.Revision, ConfigRevision: g.ConfigRevision}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.verifier.Verify(approvaladmission.Envelope(d.GroupAdmission), approvaladmission.Expected{AppID: f.e.cfg.Identity.AppID, Request: approvaladmission.Request{Scope: scope, RequestID: d.RequestID, GroupSetDigest: digest, Note: "reviewed", Actor: *d.Actor, AcceptBefore: *f.plan.ExpiresAt}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestApprovalAdmissionOpaqueSessionToSignal(t *testing.T) {
	for _, header := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent header", true: "matching header"}[header], func(t *testing.T) {
			f := newAdmissionFixture(t)
			org := ""
			if header {
				org = f.plan.OrgID
			}
			res := f.request(t, f.session.Token, org, nil)
			if res.Code != http.StatusNoContent {
				t.Fatalf("%d %s", res.Code, res.Body)
			}
			v := f.verify(t)
			if v.Claims().Source != approvaladmission.SessionSource || v.Claims().SessionID != f.session.ID.String() {
				t.Fatal("wrong session provenance")
			}
		})
	}
}
func TestApprovalAdmissionWorkspaceSwitchAndStaleTab(t *testing.T) {
	f := newAdmissionFixture(t)
	old := f.plan.OrgID
	next := authid.NewOrgID()
	f.session.OrgID = next
	if err := f.store.UpdateSession(t.Context(), f.session); err != nil {
		t.Fatal(err)
	}
	f.plan.OrgID = next.String()
	f.plan.PlacementGroups[0].OrgID = next.String()
	res := f.request(t, f.session.Token, old, nil)
	if res.Code != http.StatusConflict || f.workflow.calls != 0 {
		t.Fatalf("stale tab: %d calls=%d", res.Code, f.workflow.calls)
	}
	res = f.request(t, f.session.Token, next.String(), nil)
	if res.Code != http.StatusNoContent {
		t.Fatalf("completed switch: %d %s", res.Code, res.Body)
	}
	f.verify(t)
}
func TestApprovalAdmissionRefreshedSessionCopiedAfterMiddleware(t *testing.T) {
	f := newAdmissionFixture(t)
	f.session.ExpiresAt = time.Now().Add(time.Minute)
	if err := f.store.UpdateSession(t.Context(), f.session); err != nil {
		t.Fatal(err)
	}
	var captured approvalSource
	after := func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			captured, _ = c.Context().Value(approvalSourceKey{}).(approvalSource)
			s, _ := authmw.SessionFrom(c.Context())
			s.OrgID = authid.NewOrgID()
			s.ExpiresAt = time.Time{}
			s.Token = "mutated-secret"
			return next(c)
		}
	}
	res := f.request(t, f.session.Token, "", after)
	if res.Code != http.StatusNoContent {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	if res.Header().Get("X-Auth-Token") == "" || !captured.expires.After(time.Now().Add(5*time.Minute)) {
		t.Fatal("real refresh did not precede source capture")
	}
	v := f.verify(t)
	if v.Claims().OrgID != f.plan.OrgID {
		t.Fatal("mutable session changed source")
	}
	if strings.Contains(f.workflow.decision.GroupAdmission, "mutated-secret") {
		t.Fatal("credential retained")
	}
}
func TestApprovalAdmissionRealKeyToSignal(t *testing.T) {
	f := newAdmissionFixture(t)
	p := security.Principal{UserID: f.user.ID.String(), OrgID: f.plan.OrgID}
	created, err := f.e.keys.Create(t.Context(), p, apikeys.NewKey{Name: "approval", Access: apikeys.Write})
	if err != nil {
		t.Fatal(err)
	}
	res := f.request(t, created.Secret, "", nil)
	if res.Code != http.StatusNoContent {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	v := f.verify(t)
	if v.Claims().Source != approvaladmission.KeySource || v.Actor().KeyID != created.Key.ID || v.Claims().SessionID != "" {
		t.Fatal("wrong key provenance")
	}
}
func TestApprovalAdmissionRejectsPrincipalOnly(t *testing.T) {
	f := newAdmissionFixture(t)
	_, err := f.signer.AdmitPlanApproval(security.WithPrincipal(t.Context(), security.Principal{UserID: f.user.ID.String(), OrgID: f.plan.OrgID}), approvaladmission.Request{})
	if !errors.Is(err, approvaladmission.ErrInvalid) {
		t.Fatal("principal alone admitted")
	}
}

// These cases exercise classification after real session authentication.
func TestApprovalAdmissionUnsupportedSources(t *testing.T) {
	cases := map[string]func(context.Context, *session.Session, *user.User) context.Context{
		"missing method": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return authmw.WithAuthMethod(c, "")
		},
		"missing user": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return authmw.WithUser(c, nil)
		},
		"wrong session ID": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return authmw.WithSessionID(c, authid.NewSessionID())
		},
		"wrong context environment": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return authmw.WithEnvID(c, authid.NewEnvironmentID())
		},
		"context actors": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return authmw.WithActors(c, principal.Chain{{Kind: principal.KindUser, ID: authid.NewUserID().String()}})
		},

		"jwt": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return authmw.WithAuthMethod(c, "jwt")
		},
		"strategy": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return authmw.WithAuthMethod(c, "strategy")
		},
		"oauth": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.ClientID = "loomctl"
			return c
		},
		"scopes": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.Scopes = []string{"read"}
			return c
		},
		"audience": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.Audience = []string{"api"}
			return c
		},
		"dpop": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.DPoPJKT = "binding"
			return c
		},
		"agent": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.PrincipalKind = principal.KindAgent
			return c
		},
		"service": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.ServiceAccountID = authid.NewServiceAccountID()
			return c
		},
		"grant": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.GrantID = authid.NewAgentGrantID()
			return c
		},
		"delegation": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.DelegationID = authid.NewDelegationID()
			return c
		},
		"actor grant": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.ActorGrant = "impersonation"
			return c
		},
		"actors": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.Actors = principal.Chain{{Kind: principal.KindUser, ID: authid.NewUserID().String()}}
			return c
		},
		"impersonator": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return authmw.WithImpersonator(c, authid.NewUserID())
		},
		"wrong app": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.AppID = authid.NewAppID()
			return c
		},
		"wrong org": func(c context.Context, s *session.Session, _ *user.User) context.Context {
			s.OrgID = authid.NewOrgID()
			return c
		},
		"wrong user": func(c context.Context, _ *session.Session, u *user.User) context.Context {
			u.ID = authid.NewUserID()
			return c
		},
		"context principal": func(c context.Context, _ *session.Session, _ *user.User) context.Context {
			return security.WithPrincipal(c, security.Principal{UserID: "other"})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAdmissionFixture(t)
			router := forge.NewRouter()
			called := false
			tail := func(c forge.Context) error {
				called = true
				source, _ := c.Context().Value(approvalSourceKey{}).(approvalSource)
				if source.class != "" {
					t.Error("unsupported source captured")
				}
				return c.NoContent(http.StatusNoContent)
			}
			classify := func(next forge.Handler) forge.Handler {
				return func(c forge.Context) error {
					s, _ := authmw.SessionFrom(c.Context())
					u, _ := authmw.UserFrom(c.Context())
					c.WithContext(mutate(c.Context(), s, u))
					return f.e.requirePrincipal(next)(c)
				}
			}
			chain := f.e.sessionMiddleware()
			handler := classify(tail)
			for i := len(chain) - 1; i >= 0; i-- {
				handler = chain[i](handler)
			}
			if err := router.POST("/check", handler); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/check", nil)
			req.Header.Set("Authorization", "Bearer "+f.session.Token)
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			if !called || res.Code != http.StatusNoContent {
				t.Fatalf("ordinary traffic changed: %d %s", res.Code, res.Body)
			}
		})
	}
}

func TestApprovalAdmissionKeyPredicatesAndNoFallback(t *testing.T) {
	cases := map[string]func(*key.Key){
		"expired":       func(k *key.Key) { at := time.Now().Add(-time.Second); k.ExpiresAt = &at },
		"revoked":       func(k *key.Key) { at := time.Now(); k.RevokedAt = &at },
		"inactive":      func(k *key.Key) { k.State = key.StateSuspended },
		"readonly":      func(k *key.Key) { k.Metadata["access"] = "read" },
		"fleet":         func(k *key.Key) { k.Metadata["kind"] = "fleet" },
		"policy":        func(k *key.Key) { id := keyid.NewPolicyID(); k.PolicyID = &id },
		"wrong app":     func(k *key.Key) { k.AppID = authid.NewAppID().String() },
		"wrong org":     func(k *key.Key) { k.TenantID = authid.NewOrgID().String() },
		"wrong creator": func(k *key.Key) { k.CreatedBy = authid.NewUserID().String() },
		"no expiry":     func(k *key.Key) { k.ExpiresAt = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAdmissionFixture(t)
			p := security.Principal{UserID: f.user.ID.String(), OrgID: f.plan.OrgID}
			created, err := f.e.keys.Create(t.Context(), p, apikeys.NewKey{Name: "approval", Access: apikeys.Write})
			if err != nil {
				t.Fatal(err)
			}
			after := func(next forge.Handler) forge.Handler {
				return func(c forge.Context) error {
					id, parseErr := keyid.ParseKeyID(created.Key.ID)
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					k, getErr := f.keys.Keys().Get(c.Context(), id)
					if getErr != nil {
						t.Fatal(getErr)
					}
					mutate(k)
					if updateErr := f.keys.Keys().Update(c.Context(), k); updateErr != nil {
						t.Fatal(updateErr)
					}
					return next(c)
				}
			}
			res := f.request(t, created.Secret, "", after)
			if res.Code != http.StatusForbidden || f.workflow.calls != 0 {
				t.Fatalf("key admitted after mutation: %d calls=%d", res.Code, f.workflow.calls)
			}
		})
	}
	for _, kind := range []string{"scope", "missing", "read failure"} {
		t.Run(kind, func(t *testing.T) {
			f := newAdmissionFixture(t)
			p := security.Principal{UserID: f.user.ID.String(), OrgID: f.plan.OrgID}
			created, err := f.e.keys.Create(t.Context(), p, apikeys.NewKey{Name: "approval", Access: apikeys.Write})
			if err != nil {
				t.Fatal(err)
			}
			original := f.signer.keyFacts
			f.signer.keyFacts = func(ctx context.Context, kid string) (*key.Key, bool, error) {
				k, _, readErr := original(ctx, kid)
				switch kind {
				case "scope":
					return k, true, readErr
				case "missing":
					return nil, false, nil
				default:
					return nil, false, errors.New("backend failure")
				}
			}
			res := f.request(t, created.Secret, "", nil)
			if res.Code != http.StatusForbidden || f.workflow.calls != 0 {
				t.Fatalf("%d calls=%d", res.Code, f.workflow.calls)
			}
		})
	}
	f := newAdmissionFixture(t)
	res := f.request(t, "lp_invalid", f.plan.OrgID, nil)
	if res.Code != http.StatusUnauthorized || f.workflow.calls != 0 {
		t.Fatalf("invalid key fell through: %d", res.Code)
	}
}

func TestApprovalAdmissionSessionTemporalAndActorBinding(t *testing.T) {
	for _, kind := range []string{"expiry", "future creation", "actor", "key substitution", "principal removed"} {
		t.Run(kind, func(t *testing.T) {
			f := newAdmissionFixture(t)
			after := func(next forge.Handler) forge.Handler {
				return func(c forge.Context) error {
					source, _ := c.Context().Value(approvalSourceKey{}).(approvalSource)
					switch kind {
					case "expiry":
						f.signer.now = func() time.Time { return source.expires }
					case "future creation":
						f.signer.now = func() time.Time { return source.created.Add(-time.Nanosecond) }
					case "actor":
						p, _ := security.PrincipalFrom(c.Context())
						p.UserID = authid.NewUserID().String()
						c.WithContext(security.WithPrincipal(c.Context(), p))
					case "key substitution":
						p, _ := security.PrincipalFrom(c.Context())
						p.KeyID = keyid.NewKeyID().String()
						c.WithContext(security.WithPrincipal(c.Context(), p))
					case "principal removed":
						c.WithContext(security.WithPrincipal(c.Context(), security.Principal{}))
					}
					return next(c)
				}
			}
			res := f.request(t, f.session.Token, "", after)
			if res.Code != http.StatusForbidden || f.workflow.calls != 0 {
				t.Fatalf("%d calls=%d", res.Code, f.workflow.calls)
			}
		})
	}
}

func TestApprovalAdmissionContradictoryKeyContext(t *testing.T) {
	f := newAdmissionFixture(t)
	p := security.Principal{UserID: f.user.ID.String(), OrgID: f.plan.OrgID, KeyID: keyid.NewKeyID().String()}
	for _, ctx := range []context.Context{
		authmw.WithSession(t.Context(), f.session),
		authmw.WithAppID(t.Context(), authid.NewAppID()),
		authmw.WithOrgID(t.Context(), authid.NewOrgID()),
		authmw.WithPrincipal(t.Context(), nil),
		authmw.WithSessionID(t.Context(), f.session.ID),
		authmw.WithUser(t.Context(), f.user),
		authmw.WithPrincipal(t.Context(), &principal.Principal{Ref: principal.Ref{Kind: principal.KindUser, ID: f.user.ID.String()}}),
		authmw.WithActors(t.Context(), principal.Chain{{Kind: principal.KindUser, ID: f.user.ID.String()}}),
		authmw.WithImpersonator(t.Context(), f.user.ID),
		security.WithPrincipal(t.Context(), security.Principal{UserID: authid.NewUserID().String()}),
	} {
		captured := f.e.captureKeyApproval(ctx, p)
		source, _ := captured.Value(approvalSourceKey{}).(approvalSource)
		if source.class != "" {
			t.Fatal("mixed key and session identity accepted")
		}
	}
}

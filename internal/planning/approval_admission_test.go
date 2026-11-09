package planning

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/approvaladmission"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type sourcePlanStore struct {
	Store
	plan   *Plan
	writes int
}

func (s *sourcePlanStore) GetPlan(context.Context, string, string) (*Plan, error) { return s.plan, nil }
func (s *sourcePlanStore) Decide(_ context.Context, _, _, status, _, _ string, _ time.Time) (bool, error) {
	s.writes++
	s.plan.Status = status
	return true, nil
}

type sourcePlanAuthz struct{}

func (sourcePlanAuthz) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type sourcePlanWorkflow struct {
	store    *sourcePlanStore
	decision orchestration.Decision
	request  string
}

func (*sourcePlanWorkflow) Start(context.Context, string, string, time.Duration) error { return nil }
func (*sourcePlanWorkflow) Cancel(context.Context, string, string) error               { return nil }
func (w *sourcePlanWorkflow) Decide(_ context.Context, _, request string, d orchestration.Decision) error {
	w.request = request
	w.decision = d
	w.store.plan.Status = StatusApproved
	return nil
}

type sourcePlanAdmitter struct {
	requests []approvaladmission.Request
	err      error
}

func (a *sourcePlanAdmitter) AdmitPlanApproval(_ context.Context, r approvaladmission.Request) (approvaladmission.Envelope, error) {
	a.requests = append(a.requests, r)
	return "opaque-test-envelope", a.err
}

func sourcePlanFixture() (*Service, *sourcePlanStore, *sourcePlanWorkflow, *sourcePlanAdmitter, security.Principal) {
	id := func(prefix string) string { return typeid.MustGenerate(prefix).String() }
	p := security.Principal{UserID: id("ausr"), OrgID: id("aorg")}
	at := time.Now().UTC().Add(time.Hour)
	scope := domain.Scope{OrgID: p.OrgID, ProjectID: id("proj"), EnvironmentID: id("env")}
	plan := &Plan{ID: id("plan"), OrgID: p.OrgID, ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID, Revision: strings.Repeat("a", 64), Status: StatusReady, ExpiresAt: &at, PlacementGroups: []domain.QualifiedGroupPlan{{Scope: scope, GroupID: id("pgroup"), Generation: 1, Revision: strings.Repeat("b", 64), ConfigRevision: strings.Repeat("c", 64)}, {Scope: scope, GroupID: id("pgroup"), Generation: 2, Revision: strings.Repeat("d", 64), ConfigRevision: strings.Repeat("e", 64)}}}
	st := &sourcePlanStore{plan: plan}
	w := &sourcePlanWorkflow{store: st}
	a := &sourcePlanAdmitter{}
	s := NewService(Deps{Store: st, Authz: sourcePlanAuthz{}, Workflow: w, ApprovalAdmitter: a})
	return s, st, w, a, p
}
func TestApprovalAdmissionPlanningExactRequest(t *testing.T) {
	s, st, w, a, p := sourcePlanFixture()
	note := strings.Repeat("\x00", maxNote)
	if _, err := s.Approve(t.Context(), p, st.plan.ID, DecideInput{Revision: st.plan.Revision, Note: note}); err != nil {
		t.Fatal(err)
	}
	if len(a.requests) != 1 {
		t.Fatal("source not requested")
	}
	r := a.requests[0]
	if r.RequestID != w.request || w.decision.RequestID != w.request || w.decision.GroupAdmission != "opaque-test-envelope" || r.Note != note || r.AcceptBefore != *st.plan.ExpiresAt || r.Actor != p {
		t.Fatal("request binding lost")
	}
	if r.GroupSetDigest == "" {
		t.Fatal("full group set omitted")
	}
	changed := *st.plan
	changed.PlacementGroups = append([]domain.QualifiedGroupPlan(nil), st.plan.PlacementGroups...)
	changed.PlacementGroups = changed.PlacementGroups[:1]
	altered, err := groupApprovalRequest(&changed, p, r.RequestID, note)
	if err != nil {
		t.Fatal(err)
	}
	if altered.GroupSetDigest == r.GroupSetDigest {
		t.Fatal("omitted group not detected")
	}
}
func TestApprovalAdmissionPlanningFailureAndOrdinaryPaths(t *testing.T) {
	for _, kind := range []string{"missing source", "denied source", "deadline", "scope", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			s, st, w, a, p := sourcePlanFixture()
			switch kind {
			case "missing source":
				s.approvalAdmitter = nil
			case "denied source":
				a.err = approvaladmission.ErrInvalid
			case "deadline":
				st.plan.ExpiresAt = nil
			case "scope":
				st.plan.PlacementGroups[0].ProjectID = "foreign"
			case "duplicate":
				st.plan.PlacementGroups[1] = st.plan.PlacementGroups[0]
			}
			if _, err := s.Approve(t.Context(), p, st.plan.ID, DecideInput{Revision: st.plan.Revision}); !errors.Is(err, ErrUnavailable) {
				t.Fatal("invalid admission accepted", err)
			}
			if w.request != "" || st.writes != 0 {
				t.Fatal("refused approval escaped")
			}
		})
	}
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "ordinary approve", false: "group reject"}[approve], func(t *testing.T) {
			s, st, w, a, p := sourcePlanFixture()
			a.err = approvaladmission.ErrInvalid
			if approve {
				st.plan.PlacementGroups = nil
			}
			if _, err := s.decide(t.Context(), p, st.plan.ID, approve, DecideInput{Revision: st.plan.Revision, Note: " normal "}); err != nil {
				t.Fatal(err)
			}
			if len(a.requests) != 0 || w.decision.GroupAdmission != "" || w.decision.RequestID != "" || w.decision.Note != "normal" {
				t.Fatal("ordinary decision changed")
			}
		})
	}
}
func TestApprovalAdmissionApplyRemainsUnavailable(t *testing.T) {
	s, st, _, _, p := sourcePlanFixture()
	d := orchestration.Decision{Approve: true, Revision: st.plan.Revision, By: p.UserID, Actor: &p}
	if _, err := s.ApplyDecision(t.Context(), p.OrgID, st.plan.ID, d); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unsigned group accepted", err)
	}
	if st.writes != 0 {
		t.Fatal("unsigned group wrote acceptance")
	}
}

func TestApprovalAdmissionSignedGroupPreservesStaleSettlement(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "current remains unavailable", true: "changed inputs settle stale"}[stale], func(t *testing.T) {
			s, st, _, _, p := sourcePlanFixture()
			st.Store = catalogStore{}
			st.plan.Catalog = true
			st.plan.Environment = "production"
			doc := "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + "}\n"
			s.manifests = catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}
			s.providers = providers.Registry()
			s.clouds = serverClouds{}
			s.kinds = recipestest.Registry(t)
			_, revision, err := s.compute(t.Context(), st.plan)
			if err != nil {
				t.Fatal(err)
			}
			st.plan.Revision = revision
			r, err := groupApprovalRequest(st.plan, p, typeid.MustGenerate("dec").String(), "reviewed")
			if err != nil {
				t.Fatal(err)
			}
			key := []byte(strings.Repeat("k", 32))
			s.approvalVerifier, err = approvaladmission.NewVerifier(key)
			if err != nil {
				t.Fatal(err)
			}
			s.approvalAppID = typeid.MustGenerate("aapp").String()
			raw, err := approvaladmission.Payload(approvaladmission.Claims{Version: 1, Operation: approvaladmission.Operation, AppID: s.approvalAppID, Scope: r.Scope, RequestID: r.RequestID, GroupSetDigest: r.GroupSetDigest, Note: r.Note, Source: approvaladmission.SessionSource, UserID: p.UserID, SessionID: typeid.MustGenerate("ases").String(), AdmittedAt: time.Now().UTC(), AcceptBefore: r.AcceptBefore})
			if err != nil {
				t.Fatal(err)
			}
			prefix := "v1." + approvaladmission.KeyID(key) + "." + base64.RawURLEncoding.EncodeToString(raw)
			h := hmac.New(sha256.New, key)
			_, _ = h.Write([]byte(approvaladmission.KeyPurpose + "\x00" + prefix))
			envelope := prefix + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
			if stale {
				s.manifests = catalogManifest{revision: &manifests.Revision{Manifest: parse(t, strings.Replace(doc, "app:", "other:", 1))}}
			}
			status, err := s.ApplyDecision(t.Context(), p.OrgID, st.plan.ID, orchestration.Decision{Approve: true, Revision: revision, By: p.UserID, Actor: &p, Note: r.Note, RequestID: r.RequestID, GroupAdmission: envelope})
			if stale {
				if err != nil || status != StatusStale || st.writes != 1 {
					t.Fatalf("stale settlement: %s %v writes=%d", status, err, st.writes)
				}
			} else if !errors.Is(err, ErrUnavailable) || st.writes != 0 {
				t.Fatalf("current group escaped: %s %v writes=%d", status, err, st.writes)
			}
		})
	}
}

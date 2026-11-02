//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

type firstSendAuthority struct {
	mu         sync.Mutex
	key, grant bool
}

func (a *firstSendAuthority) Require(context.Context, security.Principal, security.Permission, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.grant {
		return security.ErrForbidden
	}
	return nil
}
func (a *firstSendAuthority) ValidatePrincipal(_ context.Context, p security.Principal) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.key || p.KeyID != "test-approval-key" {
		return security.ErrForbidden
	}
	return nil
}
func TestManagedInstalledFirstSendAuthority(t *testing.T) {
	for _, arch := range []string{"explicit", "inferred"} {
		for _, change := range []string{"pause", "revoke", "policy", "key", "grant", "inventory", "envelope", "cancel", "build_failure"} {
			t.Run(arch+"/"+change, func(t *testing.T) { managedCleanupEffectsRealAPI(t, false, "application", "fix1:"+arch+"/"+change) })
		}
	}
}
func checkInstalledFirstSend(ctx context.Context, t *testing.T, e *enrollmentServiceFixture, f *poolFixture, svc *planning.Service, p *planning.Plan, cfg *planning.ServiceConfig, dep *deployments.Deployment, runner *deployments.Runner, auth *firstSendAuthority, which string) {
	t.Helper()
	if cfg.ClusterDemand.ProfileArchitecture != strings.HasPrefix(which, "inferred/") {
		t.Fatal("wrong architecture qualification", which)
	}
	call := orchestration.ManagedWorkloadContext(ctx)
	if err := svc.ResolveCapacityIntents(call, p.OrgID, p.ID); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := f.sql.QueryRow(ctx, `SELECT result FROM loomplane_plans WHERE id=$1`, p.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	once := sync.Once{}
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(call, p.OrgID, dep.ID, cfg.ClusterDemand.Input.ActionID, nil, func(raw []byte) error {
			var resume struct {
				Apply []byte `json:"apply"`
			}
			var stage struct {
				Stage string `json:"effect_stage"`
			}
			if json.Unmarshal(raw, &resume) == nil && json.Unmarshal(resume.Apply, &stage) == nil && stage.Stage == "building" {
				once.Do(func() { close(entered); <-proceed })
			}
			return nil
		})
	}()
	defer func() {
		select {
		case <-proceed:
		default:
			close(proceed)
		}
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("never entered actual build", err)
	case <-time.After(20 * time.Second):
		t.Fatal("build did not pause")
	}
	change := strings.Split(which, "/")[1]
	var err error
	switch change {
	case "pause", "revoke", "policy":
		r, e := f.store.GetManagedResource(ctx, p.OrgID, "cluster")
		if e != nil {
			t.Fatal(e)
		}
		policy := r.ScalingPolicies["pool"]
		if change == "pause" {
			policy.PauseReason = "fixture_external_breach"
		}
		if change == "revoke" {
			policy.Revoked = true
		}
		if change == "policy" {
			policy.Revision++
		}
		r.ScalingPolicies["pool"] = policy
		r.Revision++
		_, err = f.store.ClaimManagedResource(ctx, r, r.Revision-1)
	case "key":
		auth.mu.Lock()
		auth.key = true
		auth.mu.Unlock()
	case "grant":
		auth.mu.Lock()
		auth.grant = true
		auth.mu.Unlock()
	case "inventory":
		_, err = f.sql.Exec(ctx, `UPDATE loomplane_cluster_capacity_inventory SET checked_at=$1::timestamptz,body=jsonb_set(body,'{checked_at}',to_jsonb($1::text)) WHERE resource_id='cluster'`, time.Now().Add(-3*time.Minute).UTC().Format(time.RFC3339Nano))
	case "envelope":
		_, err = f.sql.Exec(ctx, `UPDATE loomplane_cluster_capacity_envelopes SET body=jsonb_set(body,'{checked_at}',to_jsonb($1::text)) WHERE resource_id='cluster'`, time.Now().Add(-3*time.Minute).UTC().Format(time.RFC3339Nano))
	case "cancel":
		err = deployments.NewPgStore(f.db).Finish(ctx, p.OrgID, dep.ID, deployments.StatusCancelled, "during build", time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	close(proceed)
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("build did not return")
	}
	if err == nil {
		t.Fatal("authority change reached application success")
	}
	if change == "build_failure" && !strings.Contains(err.Error(), "digest-pinned") {
		t.Fatal("wrong build failure", err)
	}
	var issued, held int
	var after []byte
	if err = f.sql.QueryRow(ctx, `SELECT (SELECT count(*) FROM loomplane_managed_workload_issuances WHERE scope->>'Plan'=$1),(SELECT count(*) FROM loomplane_cluster_workload_holds WHERE body->'input'->>'plan_id'=$1 AND NOT released),(SELECT result FROM loomplane_plans WHERE id=$1)`, p.ID).Scan(&issued, &held, &after); err != nil {
		t.Fatal(err)
	}
	if issued != 0 || held != 0 || string(after) != string(before) {
		t.Fatal("failed first send changed immutable review or retained no-effect maximum", issued, held)
	}
	list, err := e.bootstrap.Kube.AppsV1().Deployments(e.attachment().Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(list.Items) != 0 {
		t.Fatal("remote application write before authority", err)
	}
	if err = svc.ResolveCapacityIntents(call, p.OrgID, p.ID); err == nil {
		t.Fatal("settled demand could reserve again")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("scope test cancelled the caller")
	}
}

// This checks the saved standing native approval through the production service,
// independently of the provider supply fixture in the installed runner cases.
type nativeFirstSendAuth struct {
	denied security.Permission
	member bool
}

func (a nativeFirstSendAuth) Require(_ context.Context, _ security.Principal, p security.Permission, _ string) error {
	if p == a.denied {
		return security.ErrForbidden
	}
	return nil
}
func (a nativeFirstSendAuth) RequireCurrentMembership(context.Context, security.Principal) error {
	if a.member {
		return security.ErrForbidden
	}
	return nil
}
func checkNativeFirstSendAuthority(t *testing.T, db *pgdriver.PgDB, svc *cloudinfra.Service) {
	t.Helper()
	svc.Store = cloudinfra.NewPgStore(db)
	check := func(want bool) {
		t.Helper()
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = svc.CurrentClusterScalingPolicyTx(t.Context(), tx, "org", "cloud", "cluster", "pool")
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if (err == nil) != want {
			t.Fatal("standing native authority", want, err)
		}
	}
	svc.CapacityPrincipal = &firstSendAuthority{}
	check(true)
	for _, permission := range []security.Permission{security.ClusterManage, security.CloudAccountManage} {
		svc.Authz = nativeFirstSendAuth{denied: permission}
		check(false)
	}
	svc.Authz = nativeFirstSendAuth{member: true}
	check(false)
	svc.Authz = enrollmentServiceAuth{}
	svc.CapacityPrincipal = &firstSendAuthority{key: true}
	check(false)
	svc.CapacityPrincipal = &firstSendAuthority{}
	check(true)
}

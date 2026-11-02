//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

type nativeAdmissionRecords interface {
	ReviewNativeTarget(context.Context, string, string, string, string) (recovery.NativeRestoreTarget, error)
	CreateReviewedNativeRestore(context.Context, *recovery.NativeRestore, string) (*recovery.NativeRestore, error)
	CheckNativeTarget(context.Context, *recovery.NativeRestore) error
}

func nativeAdmissionFixture(t *testing.T) (nativePointFixture, nativeAdmissionRecords, *recovery.NativeRestore, string) {
	t.Helper()
	f := nativePointHistoryFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := &connections.Connection{ID: "native-target-account", OrgID: "org", Cloud: connections.GCP, Name: "Native copy target", Account: "target-project", Details: map[string]string{"project_id": "target-project"}, Status: connections.StatusReady, CreatedBy: "operator", CreatedAt: now, UpdatedAt: now}
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	sourceAccount := *c
	sourceAccount.ID, sourceAccount.Name, sourceAccount.Account = "source-account", "Native source", "source-project"
	sourceAccount.Details = map[string]string{"project_id": "source-project"}
	if err := connections.NewPgStore(f.db).CreateConnection(t.Context(), &sourceAccount); err != nil {
		t.Fatal(err)
	}
	clouds := cloudprofiles.NewPgStore(f.db)
	if err := clouds.CreateProfile(t.Context(), &cloudprofiles.Profile{ID: "native-cloud", OrgID: "org", ProjectID: "project", Name: "Native target", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := clouds.CreateTarget(t.Context(), &cloudprofiles.Target{ID: "native-target", OrgID: "org", ProjectID: "project", CloudID: "native-cloud", Name: "recovery", DisplayName: "Recovery", Runtime: "managed", Provider: "cloud-sql", ConnectionID: c.ID, Region: "us-central1", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := clouds.SetProjectGrant(t.Context(), "org", "native-cloud", "project", []string{"native-target"}, false); err != nil {
		t.Fatal(err)
	}
	b := f.source.Binding
	b.Provider, b.ConnectionID, b.ExternalID, b.PhysicalID, b.Region = "cloud-sql", "source-account", "source-project:us-central1:source", "original-source", "us-central1"
	profile, err := manageddb.Postgres("cloud-sql", "17")
	if err != nil {
		t.Fatal(err)
	}
	b.Config, err = json.Marshal(planning.ResourceConfig{Provider: "cloud-sql", Kind: "postgres", Version: "17", Region: b.Region, Managed: &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: sourceAccount.ID, AccountIdentity: connections.AccountIdentity(connections.GCP, sourceAccount.Account, sourceAccount.Details)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = planning.NewPgStore(f.db).PutBinding(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	sources, err := f.store.ListNativeSources(t.Context(), "org", "project", recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources.Sources {
		if source.Provider == "cloud-sql" {
			f.source = source
		}
	}
	point := recovery.NativePoint{ProviderID: "projects/source-project/backups/backup", Name: "backup", Kind: "ENHANCED", Status: "SUCCESSFUL", Engine: "postgres", Version: "17", CreatedAt: now.Add(-time.Hour), Encrypted: true}
	if err = f.refresh(t.Context(), []recovery.NativePoint{point}); err != nil {
		t.Fatal(err)
	}
	points, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(points.Points) != 1 {
		t.Fatal("missing native admission point", err)
	}
	store, ok := any(f.store).(nativeAdmissionRecords)
	if !ok {
		t.Fatal("native admission does not enforce project-granted Cloud targets")
	}
	target, err := store.ReviewNativeTarget(t.Context(), "org", "project", "native-cloud", "native-target")
	if err != nil {
		t.Fatal(err)
	}
	r := &recovery.NativeRestore{ID: "reviewed-copy", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", SourceID: f.source.ID, PointID: points.Points[0].ID, Point: points.Points[0], Source: f.source.Binding, Target: target, RequestID: "reviewed-request", CreatedBy: "operator", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	raw, _ := json.Marshal(f.source.Binding)
	digest := sha256.Sum256(raw)
	return f, store, r, hex.EncodeToString(digest[:])
}

func TestNativeAdmissionPinsGrantedCloudTargetAndRejectsChangedTTLReplay(t *testing.T) {
	f, store, request, digest := nativeAdmissionFixture(t)
	raw, _ := json.Marshal(request.Target)
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"cloud_id": "native-cloud", "target_id": "native-target", "cloud_revision": float64(1), "target_revision": float64(1), "grant_revision": float64(2), "native_project": "target-project", "region": "us-central1"} {
		if fields[key] != want {
			t.Fatal("target review omitted a placement pin", key, fields[key])
		}
	}
	got, err := store.CreateReviewedNativeRestore(t.Context(), request, digest)
	if err != nil || got.Status != "queued" || got.Target != request.Target {
		t.Fatal("reviewed admission lost its immutable target", got, err)
	}
	replay := *request
	replay.ID = "replay-copy"
	replay.CreatedAt = replay.CreatedAt.Add(time.Minute)
	replay.ExpiresAt = replay.ExpiresAt.Add(time.Minute)
	if got, err = store.CreateReviewedNativeRestore(t.Context(), &replay, digest); err != nil || got.ID != request.ID {
		t.Fatal("same duration replay created another copy", got, err)
	}
	replay.ExpiresAt = replay.ExpiresAt.Add(time.Hour)
	if _, err = store.CreateReviewedNativeRestore(t.Context(), &replay, digest); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("request replay silently changed its cost/TTL bound", err)
	}
	replay.ExpiresAt = replay.CreatedAt.Add(time.Hour)
	replay.CreatedBy = "another-operator"
	if _, err = store.CreateReviewedNativeRestore(t.Context(), &replay, digest); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("request replay silently changed its authorizing caller", err)
	}
	if _, err = store.ReviewNativeTarget(t.Context(), "foreign", "project", "native-cloud", "native-target"); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("foreign organization reviewed native target", err)
	}
	if _, err = cloudprofiles.NewPgStore(f.db).SetProjectGrant(t.Context(), "org", "native-cloud", "project", nil, true); err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNativeTarget(t.Context(), request); err == nil {
		t.Fatal("revoked project grant still authorizes native copy mutations")
	}
}

func TestNativeAdmissionRejectsChangedReviewedInputsWithoutRecordingCopy(t *testing.T) {
	for _, scenario := range []string{"source", "source-digest", "target-region", "target-revision", "cloud-revision", "grant-revision", "revoked", "excluded", "account", "not-ready", "wrong-provider", "forged-pin", "unreviewed-path"} {
		t.Run(scenario, func(t *testing.T) {
			f, store, request, digest := nativeAdmissionFixture(t)
			var query string
			switch scenario {
			case "source":
				query = `UPDATE loomplane_native_sources SET source=jsonb_set(source,'{config,version}','"18"') WHERE provider='cloud-sql'`
			case "source-digest":
				digest = fmt.Sprintf("%064x", 0)
			case "target-region":
				query = `UPDATE loomplane_cloud_targets SET region='europe-west1',revision=revision+1 WHERE id='native-target'`
			case "target-revision":
				query = `UPDATE loomplane_cloud_targets SET revision=revision+1 WHERE id='native-target'`
			case "cloud-revision":
				query = `UPDATE loomplane_cloud_profiles SET revision=revision+1 WHERE id='native-cloud'`
			case "grant-revision":
				query = `UPDATE loomplane_cloud_project_grants SET revision=revision+1 WHERE cloud_id='native-cloud'`
			case "revoked":
				query = `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE cloud_id='native-cloud'`
			case "excluded":
				query = `UPDATE loomplane_cloud_project_grants SET target_ids=ARRAY['another-target'],revision=revision+1 WHERE cloud_id='native-cloud'`
			case "account":
				query = `UPDATE loomplane_cloud_connections SET details='{"project_id":"changed-project"}' WHERE id='native-target-account'`
			case "not-ready":
				query = `UPDATE loomplane_cloud_connections SET status='failed' WHERE id='native-target-account'`
			case "wrong-provider":
				query = `UPDATE loomplane_cloud_targets SET provider='gke',revision=revision+1 WHERE id='native-target'`
			case "forged-pin":
				request.Target.GrantRevision++
			case "unreviewed-path":
			}
			if query != "" {
				if _, err := f.sql.Exec(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if scenario == "unreviewed-path" {
				_, err = f.store.CreateNativeRestore(t.Context(), request)
			} else {
				_, err = store.CreateReviewedNativeRestore(t.Context(), request, digest)
			}
			if err == nil {
				t.Fatal("stale or forged review admitted a native copy", scenario)
			}
			if _, err = f.store.GetNativeRestore(t.Context(), "org", request.ID); !errors.Is(err, recovery.ErrNotFound) {
				t.Fatal("rejected review retained a copy or connection reservation", err)
			}
		})
	}
}

type nativeAdmissionSelection struct{ calls int }

func (*nativeAdmissionSelection) NativeSnapshots(context.Context, *planning.Binding) ([]recovery.NativePoint, bool, error) {
	return nil, false, errors.New("unexpected inventory refresh")
}

func (s *nativeAdmissionSelection) CheckNativeSnapshot(_ context.Context, _ *planning.Binding, point recovery.NativePoint) (*recovery.NativePoint, error) {
	s.calls++
	return &point, nil
}

func TestNativeAdmissionWorkerFencesRevokedPlacementWithoutStrandingCleanup(t *testing.T) {
	for _, scenario := range []string{"before-worker", "during-worker"} {
		t.Run(scenario, func(t *testing.T) {
			f, store, request, digest := nativeAdmissionFixture(t)
			r, err := store.CreateReviewedNativeRestore(t.Context(), request, digest)
			if err != nil {
				t.Fatal(err)
			}
			revoke := func() {
				t.Helper()
				if _, err = cloudprofiles.NewPgStore(f.db).SetProjectGrant(t.Context(), "org", "native-cloud", "project", nil, true); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "before-worker" {
				revoke()
			}
			selection := &nativeAdmissionSelection{}
			calls, mutations := 0, 0
			s := &recovery.NativeService{Store: f.store, Copies: f.store, Cleanups: f.store, Admission: store, Authz: &nativeCopyAuthorization{}, Projects: projects.NewPgStore(f.db), Backend: selection}
			s.CopyBackend = nativeCopyExecution(func(ctx context.Context, _ security.Principal, _ recovery.NativeRestore, c recovery.NativeCopyControl) (recovery.NativeRestoreResult, error) {
				calls++
				if scenario == "during-worker" {
					revoke()
				}
				if guardErr := c.Check(ctx); guardErr != nil {
					return recovery.NativeRestoreResult{}, guardErr
				}
				mutations++
				return recovery.NativeRestoreResult{}, errors.New("native mutation unexpectedly authorized")
			})
			if err = s.RunCopy(t.Context(), "org", r.ID); err == nil || mutations != 0 || scenario == "before-worker" && (calls != 0 || selection.calls != 0) {
				t.Fatal("revoked placement reached native mutation", err, calls, mutations, selection.calls)
			}
			s.EnqueueCleanup = func(context.Context, string, string) error { return nil }
			s.Authz = &nativeCleanupAuthorization{actor: "cleanup-admin"}
			if _, err = s.RequestCopyCleanup(t.Context(), security.Principal{OrgID: "org", UserID: "cleanup-admin"}, r.ID); err != nil {
				t.Fatal(err)
			}
			cleanupCalls := 0
			s.CleanupBackend = nativeDeleteExecution(func(ctx context.Context, _ security.Principal, _ recovery.NativeRestore, c recovery.NativeWorkerControl) (recovery.NativeCleanupResult, error) {
				if guardErr := c.Check(ctx); guardErr != nil {
					return recovery.NativeCleanupResult{}, guardErr
				}
				cleanupCalls++
				return recovery.NativeCleanupResult{ConfirmedAbsent: true}, nil
			})
			if err = s.RunCleanup(t.Context(), "org", r.ID); err != nil || cleanupCalls != 1 {
				t.Fatal("revoked placement stranded retained cleanup", err, cleanupCalls)
			}
		})
	}
}

func TestNativeAdmissionWaitsForCurrentGrantBeforeAtomicInsert(t *testing.T) {
	f, store, request, digest := nativeAdmissionFixture(t)
	lock, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	if _, err = lock.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "cloud-organization/org"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, createErr := store.CreateReviewedNativeRestore(t.Context(), request, digest)
		done <- createErr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err = f.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE a.datname=$1 AND l.locktype='advisory' AND NOT l.granted)`, f.sql.Config().Database).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err = <-done:
			t.Fatal("admission ignored the Cloud authority transaction", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("admission never waited for the organization authority lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = lock.Exec(t.Context(), `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE cloud_id='native-cloud'`); err != nil {
		t.Fatal(err)
	}
	if err = lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("admitted a copy after its grant was revoked")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not finish after authority transaction committed")
	}
	if _, err = f.store.GetNativeRestore(t.Context(), "org", request.ID); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("revoked grant left an admitted copy", err)
	}
}

func TestNativeCopyQueriesKeepRequestAndPageCursorsInsideTheirScope(t *testing.T) {
	f, store, request, digest := nativeAdmissionFixture(t)
	queries, ok := any(f.store).(recovery.NativeCopyQueries)
	if !ok {
		t.Fatal("native copy queries are missing")
	}
	for i, id := range []string{"copy-a", "copy-b", "copy-c"} {
		r := *request
		r.ID, r.RequestID = id, "request-"+id
		r.CreatedAt = r.CreatedAt.Add(time.Duration(i) * time.Microsecond)
		r.ExpiresAt = r.CreatedAt.Add(time.Hour)
		if _, err := store.CreateReviewedNativeRestore(t.Context(), &r, digest); err != nil {
			t.Fatal(err)
		}
	}
	first, err := queries.ListNativeCopies(t.Context(), "org", "project", recovery.NativePageInput{EnvironmentID: "environment", Limit: 1})
	if err != nil || len(first.Copies) != 1 || first.Copies[0].ID != "copy-c" || first.NextBefore != "copy-c" {
		t.Fatal("copy page lost its newest record or cursor", first, err)
	}
	next, err := queries.ListNativeCopies(t.Context(), "org", "project", recovery.NativePageInput{EnvironmentID: "environment", Limit: 1, Before: first.NextBefore})
	if err != nil || len(next.Copies) != 1 || next.Copies[0].ID != "copy-b" || next.NextBefore != "copy-b" {
		t.Fatal("copy cursor skipped or repeated a record", next, err)
	}
	for _, scope := range [][3]string{{"foreign", "project", "environment"}, {"org", "foreign", "environment"}, {"org", "project", "other-environment"}} {
		page, pageErr := queries.ListNativeCopies(t.Context(), scope[0], scope[1], recovery.NativePageInput{EnvironmentID: scope[2]})
		if pageErr != nil || len(page.Copies) != 0 {
			t.Fatal("copy list escaped its scope", scope, page, pageErr)
		}
		if _, pageErr = queries.ListNativeCopies(t.Context(), scope[0], scope[1], recovery.NativePageInput{EnvironmentID: scope[2], Before: "copy-c"}); !errors.Is(pageErr, recovery.ErrNotFound) {
			t.Fatal("foreign copy cursor was accepted", scope, pageErr)
		}
	}
	for _, scope := range [][2]string{{"foreign", "project"}, {"org", "foreign"}} {
		if _, err = queries.GetNativeRestoreByRequest(t.Context(), scope[0], scope[1], "request-copy-a"); !errors.Is(err, recovery.ErrNotFound) {
			t.Fatal("copy request escaped its scope", scope, err)
		}
	}
}

//go:build integration

package integration

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	lpclient "github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetAPIClientContract(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.Concurrency = 1
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Drift.Interval = 0
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	verifyFleetAPIClient(t, r, cfg)
}

func verifyFleetAPIClient(t *testing.T, r *running, cfg config.Config) {
	t.Helper()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "fleet-operator@example.test", "Synthetic-fleet-password-9")
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Fleet Example", "slug": "fleet-example"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var session struct {
		UserID string `json:"user_id"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &session)
	var project projects.Project
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Fleet Example"}, &project)
	waitReady(t, c, project.ID)
	app, ok := r.app.(forge.App)
	if !ok {
		t.Fatal("fixture does not expose the registered app")
	}
	service, err := forge.InjectType[*fleets.Service](app.Container())
	if err != nil {
		t.Fatal(err)
	}
	principal := security.Principal{OrgID: org.ID, UserID: session.UserID, Kind: security.KindUser}
	f, err := service.CreateFleet(t.Context(), principal, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	db, err := forge.InjectType[*grove.DB](app.Container())
	if err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.Unwrap(db)
	cloud := &cloudprofiles.Profile{ID: uuid.NewString(), OrgID: org.ID, ProjectID: project.ID, Name: "Example capacity", Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err = cloudprofiles.NewPgStore(pg).CreateProfile(t.Context(), cloud); err != nil {
		t.Fatal(err)
	}
	stage, err := service.CreateStage(t.Context(), principal, f.ID, fleet.CreateStageInput{Name: "production", EnvironmentKind: "production", AllowedCloudIDs: []string{cloud.ID}, MaxInstances: 1})
	if err != nil {
		t.Fatal(err)
	}
	release, err := service.PublishRelease(t.Context(), principal, f.ID, fleetReleaseDocument())
	if err != nil {
		t.Fatal(err)
	}
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "Fleet contract", "access": "write"}, &key)
	sdk := lpclient.New(r.srv.URL, key.Secret)
	input := fleet.CreateInstanceInput{StageID: stage.ID, ReleaseID: release.ID, ProfileID: "small", ExternalRef: "sample-a", IdempotencyKey: "sample-request"}
	accepted, err := sdk.CreateFleetInstance(t.Context(), project.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := sdk.CreateFleetInstance(t.Context(), project.ID, input)
	if err != nil || again.Instance.ID != accepted.Instance.ID || again.Operation.ID != accepted.Operation.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if accepted.Instance.Status != "waiting_approval" || accepted.Instance.ObservedGeneration != 0 || accepted.Operation.Status != "waiting_approval" {
		t.Fatalf("acceptance claimed execution: %+v", accepted)
	}
	got, err := sdk.GetFleetInstance(t.Context(), project.ID, accepted.Instance.ID)
	if err != nil || got.ID != accepted.Instance.ID {
		t.Fatalf("instance: %+v %v", got, err)
	}
	page, err := sdk.ListFleetInstances(t.Context(), project.ID, fleet.ListInstancesInput{ExternalRef: "sample-a", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != got.ID || page.NextCursor != "" {
		t.Fatalf("list: %+v %v", page, err)
	}
	op, err := sdk.GetFleetOperation(t.Context(), project.ID, accepted.Operation.ID)
	if err != nil || op.ID != accepted.Operation.ID {
		t.Fatalf("operation: %+v %v", op, err)
	}
	changed := input
	changed.ProfileID = "unknown"
	if _, err = sdk.CreateFleetInstance(t.Context(), project.ID, changed); err == nil {
		t.Fatal("changed replay accepted")
	}
	changed = input
	changed.IdempotencyKey = "another-request"
	if _, err = sdk.CreateFleetInstance(t.Context(), project.ID, changed); !errors.Is(err, lpclient.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	changed = input
	changed.ExternalRef = "sample-b"
	changed.IdempotencyKey = "sample-b-request"
	if _, err = sdk.CreateFleetInstance(t.Context(), project.ID, changed); err == nil {
		t.Fatal("quota bypass")
	} else {
		var failure *lpclient.Error
		if !errors.As(err, &failure) || failure.Status != http.StatusTooManyRequests {
			t.Fatalf("quota: %v", err)
		}
	}
	if _, err = sdk.GetFleetInstance(t.Context(), uuid.NewString(), got.ID); !errors.Is(err, lpclient.ErrNotFound) {
		t.Fatalf("hidden project: %v", err)
	}
	var reader keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "Fleet read", "access": "read"}, &reader)
	if _, err = lpclient.New(r.srv.URL, reader.Secret).CreateFleetInstance(t.Context(), project.ID, input); !errors.Is(err, lpclient.ErrForbidden) {
		t.Fatalf("read-only create: %v", err)
	}
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/api-keys/"+key.Key.ID, nil, nil)
	if _, err = sdk.GetFleetInstance(t.Context(), project.ID, got.ID); !errors.Is(err, lpclient.ErrUnauthorized) {
		t.Fatalf("revoked key: %v", err)
	}
}

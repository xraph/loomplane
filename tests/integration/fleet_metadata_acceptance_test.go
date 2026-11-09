//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
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

type fleetMetadataScope struct {
	sdk       *lpclient.Client
	reader    *lpclient.Client
	fleetID   string
	projectID string
	stageID   string
	releaseID string
}

func (f fleetMetadataScope) input(index int) fleet.CreateInstanceInput {
	ref := fmt.Sprintf("sample-%04d", index)
	return fleet.CreateInstanceInput{StageID: f.stageID, ReleaseID: f.releaseID, ProfileID: "small", ExternalRef: ref, IdempotencyKey: "metadata-" + ref}
}

// TestFleetAPIMetadataLadder exercises registered routes and durable metadata only.
// The fixture never creates grants, execution authority or provider credentials.
func TestFleetAPIMetadataLadder(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	databaseURL, err := url.Parse(cfg.Database.URL)
	if err != nil {
		t.Fatal("parse generated metadata database URL")
	}
	t.Logf("fleet_metadata_database %s", strings.TrimPrefix(databaseURL.Path, "/"))
	cfg.Orchestration.Concurrency = 1
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Drift.Interval = 0
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "metadata-operator@example.test", "Synthetic-metadata-password-9")
	app, ok := r.app.(forge.App)
	if !ok {
		t.Fatal("fixture does not expose the registered app")
	}
	service, err := forge.InjectType[*fleets.Service](app.Container())
	if err != nil {
		t.Fatal(err)
	}
	db, err := forge.InjectType[*grove.DB](app.Container())
	if err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.Unwrap(db)
	primary := newFleetMetadataScope(t, c, service, pg, "metadata-primary", 1000)
	neighbor := newFleetMetadataScope(t, c, service, pg, "metadata-neighbor", 1)
	other, err := neighbor.sdk.CreateFleetInstance(t.Context(), neighbor.projectID, neighbor.input(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = primary.reader.GetFleetInstance(t.Context(), neighbor.projectID, other.Instance.ID); !errors.Is(err, lpclient.ErrNotFound) {
		t.Fatalf("foreign organization instance: %v", err)
	}
	if _, err = primary.reader.ListFleetInstances(t.Context(), neighbor.projectID, fleet.ListInstancesInput{Limit: 1}); !errors.Is(err, lpclient.ErrNotFound) {
		t.Fatalf("foreign organization list: %v", err)
	}

	accepted := make([]fleet.OperationResult, 0, 1000)
	environments := make(map[string]bool, 1000)
	for _, total := range []int{10, 100, 1000} {
		if !t.Run(fmt.Sprintf("records_%d", total), func(t *testing.T) {
			started := time.Now()
			newRecords := total - len(accepted)
			for len(accepted) < total {
				result, createErr := primary.sdk.CreateFleetInstance(t.Context(), primary.projectID, primary.input(len(accepted)))
				if createErr != nil {
					t.Fatalf("create %d: %v", len(accepted), createErr)
				}
				if result.Instance.Status != "waiting_approval" || result.Operation.Status != "waiting_approval" || result.Instance.ObservedGeneration != 0 || result.Instance.ObservedReleaseID != "" {
					t.Fatalf("metadata request acquired execution state: %+v", result)
				}
				if result.Instance.FleetID != primary.fleetID || result.Instance.StageID != primary.stageID || result.Instance.EnvironmentID == "" || environments[result.Instance.EnvironmentID] {
					t.Fatalf("instance scope or environment identity mismatch: %+v", result.Instance)
				}
				environments[result.Instance.EnvironmentID] = true
				accepted = append(accepted, *result)
			}
			createDuration := time.Since(started)
			for _, limit := range []int{0, 73, 200} {
				verifyFleetMetadataPages(t, primary, accepted, limit, newRecords, createDuration)
			}
			if total == 10 {
				verifyFleetMetadataPages(t, primary, accepted, 1, newRecords, createDuration)
			}
			verifyFleetMetadataFilters(t, primary, accepted)
			verifyFleetMetadataRows(t, pg, primary, total)
		}) {
			return
		}
	}
	for _, in := range []fleet.ListInstancesInput{{Limit: -1}, {Limit: 201}, {After: "invalid-cursor"}} {
		_, err = primary.reader.ListFleetInstances(t.Context(), primary.projectID, in)
		var response *lpclient.Error
		if !errors.As(err, &response) || response.Status != http.StatusUnprocessableEntity {
			t.Fatalf("invalid pagination accepted: %+v: %v", in, err)
		}
	}
	_, err = primary.sdk.CreateFleetInstance(t.Context(), primary.projectID, primary.input(1000))
	var response *lpclient.Error
	if !errors.As(err, &response) || response.Status != http.StatusTooManyRequests {
		t.Fatalf("metadata stage bound bypassed: %v", err)
	}
	verifyFleetMetadataRows(t, pg, primary, 1000)
	store := fleets.NewPgStore(pg)
	for _, limit := range []int{1, 7, 100} {
		items, pendingErr := store.PendingOperations(t.Context(), limit)
		if pendingErr != nil || len(items) != limit {
			t.Fatalf("bounded pending operation read %d: items=%d error=%v", limit, len(items), pendingErr)
		}
	}
	for _, limit := range []int{0, 101} {
		if _, pendingErr := store.PendingOperations(t.Context(), limit); !errors.Is(pendingErr, fleets.ErrInvalid) {
			t.Fatalf("unbounded pending operation read %d: %v", limit, pendingErr)
		}
	}
}

func newFleetMetadataScope(t *testing.T, c *client, service *fleets.Service, db *pgdriver.PgDB, name string, maximum int) fleetMetadataScope {
	t.Helper()
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": name, "slug": name}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var session struct {
		UserID string `json:"user_id"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &session)
	var project projects.Project
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": name}, &project)
	waitReady(t, c, project.ID)
	principal := security.Principal{OrgID: org.ID, UserID: session.UserID, Kind: security.KindUser}
	f, err := service.CreateFleet(t.Context(), principal, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cloud := &cloudprofiles.Profile{ID: uuid.NewString(), OrgID: org.ID, ProjectID: project.ID, Name: "Synthetic metadata placement", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = cloudprofiles.NewPgStore(db).CreateProfile(t.Context(), cloud); err != nil {
		t.Fatal(err)
	}
	stage, err := service.CreateStage(t.Context(), principal, f.ID, fleet.CreateStageInput{Name: "qualification", EnvironmentKind: "staging", AllowedCloudIDs: []string{cloud.ID}, MaxInstances: maximum})
	if err != nil {
		t.Fatal(err)
	}
	release, err := service.PublishRelease(t.Context(), principal, f.ID, fleetReleaseDocument())
	if err != nil {
		t.Fatal(err)
	}
	var writer, reader keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "Metadata writer", "access": "write"}, &writer)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "Metadata reader", "access": "read"}, &reader)
	return fleetMetadataScope{sdk: lpclient.New(c.base, writer.Secret), reader: lpclient.New(c.base, reader.Secret), fleetID: f.ID, projectID: project.ID, stageID: stage.ID, releaseID: release.ID}
}

func verifyFleetMetadataPages(t *testing.T, scope fleetMetadataScope, accepted []fleet.OperationResult, requestedLimit, newRecords int, createDuration time.Duration) {
	t.Helper()
	limit := requestedLimit
	if limit == 0 {
		limit = 50
	}
	expected := make([]string, 0, len(accepted))
	for _, result := range accepted {
		expected = append(expected, result.Instance.ID)
	}
	slices.Sort(expected)
	seen := make([]string, 0, len(expected))
	cursor := ""
	pages := 0
	started := time.Now()
	for pages <= len(expected)/limit+1 {
		page, err := scope.reader.ListFleetInstances(t.Context(), scope.projectID, fleet.ListInstancesInput{Limit: requestedLimit, After: cursor, Status: "waiting_approval"})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Items) == 0 || len(page.Items) > limit {
			t.Fatalf("empty or oversized page: %d (limit %d)", len(page.Items), limit)
		}
		for _, item := range page.Items {
			if item.FleetID != scope.fleetID || item.StageID != scope.stageID || item.Status != "waiting_approval" || item.ID <= cursor {
				t.Fatalf("wrong scope/status or non-advancing item: %+v", item)
			}
			seen = append(seen, item.ID)
		}
		last := page.Items[len(page.Items)-1].ID
		if page.NextCursor == "" {
			cursor = last
			break
		}
		if page.NextCursor != last || len(page.Items) != limit {
			t.Fatalf("invalid continuation: %+v", page)
		}
		cursor = page.NextCursor
	}
	if !slices.Equal(seen, expected) || pages != (len(expected)+limit-1)/limit {
		t.Fatalf("pagination lost, duplicated or reordered records: got %d in %d pages, want %d", len(seen), pages, len(expected))
	}
	duration := time.Since(started)
	empty, err := scope.reader.ListFleetInstances(t.Context(), scope.projectID, fleet.ListInstancesInput{Limit: requestedLimit, After: cursor})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("terminal continuation: %+v %v", empty, err)
	}
	report := map[string]any{"records": len(expected), "new_records": newRecords, "page_limit": limit, "pages": pages, "create_duration_ns": createDuration.Nanoseconds(), "list_duration_ns": duration.Nanoseconds(), "live_qualified": false}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fleet_metadata_measurement %s", raw)
}

func verifyFleetMetadataFilters(t *testing.T, scope fleetMetadataScope, accepted []fleet.OperationResult) {
	t.Helper()
	page, err := scope.reader.ListFleetInstances(t.Context(), scope.projectID, fleet.ListInstancesInput{ExternalRef: "sample-0000", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != accepted[0].Instance.ID || page.NextCursor != "" {
		t.Fatalf("external reference filter: %+v %v", page, err)
	}
	empty, err := scope.reader.ListFleetInstances(t.Context(), scope.projectID, fleet.ListInstancesInput{Status: "ready", Limit: 1})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("unknown readiness became ready: %+v %v", empty, err)
	}
	replay, err := scope.sdk.CreateFleetInstance(t.Context(), scope.projectID, scope.input(0))
	if err != nil || replay.Instance.ID != accepted[0].Instance.ID || replay.Operation.ID != accepted[0].Operation.ID {
		t.Fatalf("replay at %d records: %+v %v", len(accepted), replay, err)
	}
	for _, result := range []fleet.OperationResult{accepted[0], accepted[len(accepted)-1]} {
		op, getErr := scope.reader.GetFleetOperation(t.Context(), scope.projectID, result.Operation.ID)
		if getErr != nil || op.ID != result.Operation.ID || op.InstanceID != result.Instance.ID || op.Status != "waiting_approval" {
			t.Fatalf("operation identity/state: %+v %v", op, getErr)
		}
	}
}

func verifyFleetMetadataRows(t *testing.T, db *pgdriver.PgDB, scope fleetMetadataScope, expected int) {
	t.Helper()
	var instances, operations, inputs, outbox, environments, plans, authority, namespaces, resources int
	err := db.QueryRow(t.Context(), `SELECT
(SELECT count(*) FROM loomplane_fleet_instances WHERE fleet_id=$1),
(SELECT count(*) FROM loomplane_fleet_operations WHERE fleet_id=$1),
(SELECT count(*) FROM loomplane_fleet_operation_inputs WHERE fleet_id=$1),
(SELECT count(*) FROM loomplane_fleet_operation_outbox WHERE fleet_id=$1),
(SELECT count(DISTINCT environment_id) FROM loomplane_fleet_instances WHERE fleet_id=$1),
(SELECT count(*) FROM loomplane_plans WHERE project_id=$2),
(SELECT count(*) FROM loomplane_fleet_authorities WHERE fleet_id=$1),
(SELECT count(*) FROM loomplane_fleet_namespaces WHERE fleet_id=$1),
(SELECT count(*) FROM loomplane_fleet_resource_bindings WHERE fleet_id=$1)`, scope.fleetID, scope.projectID).Scan(&instances, &operations, &inputs, &outbox, &environments, &plans, &authority, &namespaces, &resources)
	if err != nil {
		t.Fatal(err)
	}
	if instances != expected || operations != expected || inputs != expected || outbox != expected || environments != expected {
		t.Fatalf("metadata reservation mismatch: instances=%d operations=%d inputs=%d outbox=%d environments=%d want=%d", instances, operations, inputs, outbox, environments, expected)
	}
	if plans != 0 || authority != 0 || namespaces != 0 || resources != 0 {
		t.Fatalf("metadata-only fixture acquired execution work: plans=%d authorities=%d namespaces=%d resources=%d", plans, authority, namespaces, resources)
	}
}

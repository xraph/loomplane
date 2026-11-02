//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func TestNativeCloudSQLPITRCopyBackend(t *testing.T) {
	for _, scenario := range []string{"complete", "network-restart", "network-timeout", "admin-timeout", "lost-clone-operation", "lost-network-operation", "lost-admin-operation", "failed-clone", "cleanup-replaced", "delete-timeout", "lost-delete-operation", "credentials-replaced", "network-drift", "source-changed"} {
		t.Run(scenario, func(t *testing.T) { runCloudSQLPITRCopy(t, scenario) })
	}
}

func TestNativeCloudSQLPITRHTTPCLIAndDispatchRetainsExactTime(t *testing.T) {
	runCloudSQLPITRCopy(t, "http-cli-dispatch")
}

func TestNativeCloudSQLDeletedPITRHTTPCLIAndDispatchRetainsExactTime(t *testing.T) {
	runCloudSQLPITRCopy(t, "http-cli-dispatch-deleted")
}

func runCloudSQLPITRCopy(t *testing.T, scenario string) {
	t.Helper()
	f := nativePointHistoryFixture(t)
	sql := nativeSQLFixtureFor(t)
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	selected := now.Add(-time.Hour).Add(123456789 * time.Nanosecond)
	var deleted *time.Time
	if strings.HasSuffix(scenario, "-deleted") {
		at := now.Add(-30 * time.Second).Add(987654321 * time.Nanosecond)
		deleted = &at
	}
	source := gcp.SQLInstance{Kind: "sql#instance", Name: "source-database", Project: "source-project", Region: "us-central1", ConnectionName: "source-project:us-central1:source-database", CreateTime: now.Add(-24 * time.Hour).Format(time.RFC3339Nano), DatabaseVersion: "POSTGRES_17", State: "RUNNABLE"}
	disabled := false
	source.Settings.Version, source.Settings.Edition, source.Settings.Availability = "1", "ENTERPRISE", "ZONAL"
	source.Settings.Tier, source.Settings.DataDiskSizeGB, source.Settings.DataDiskType = profile.ComputeClass, 10, "PD_SSD"
	source.Settings.StorageAutoResize, source.Settings.ConnectorEnforcement = &disabled, "REQUIRED"
	source.Settings.DataAPIAccess = "DISALLOW_DATA_API"
	source.Settings.Backup = &gcp.SQLBackupSettings{Enabled: true, PITREnabled: true, RecoveryDays: 7}
	source.Settings.IPConfiguration = &gcp.SQLIPConfiguration{IPv4Enabled: &disabled, PSC: &gcp.SQLPSCSettings{Enabled: true, AllowedProjects: []string{"app-project"}}}
	source.Settings.UserLabels = map[string]string{"managed-by": "loomplane", gcp.SQLIntentLabel: "source-intent"}
	physical, err := source.Incarnation(source.Project, source.Region, source.Name)
	if err != nil {
		t.Fatal(err)
	}
	route := manageddb.CloudSQLRecoveryNetwork{ConsumerProject: "worker-project", Network: "projects/worker-project/global/networks/recovery"}
	conn := &connections.Connection{ID: "source-account", OrgID: "org", Cloud: connections.GCP, Account: "source-project", Status: connections.StatusReady, Details: map[string]string{"project_id": "source-project", "cloud_sql_recovery_network": route.Network}}
	if err = connections.NewPgStore(f.db).CreateConnection(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	identity := connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)
	cfg := planning.ResourceConfig{Provider: "cloud-sql", Kind: "postgres", Version: "17", Region: source.Region, Managed: &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: conn.ID, AccountIdentity: identity, CloudSQLPSC: manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/us-central1/forwardingRules/database"}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	binding := planning.Binding{ID: "psc-source", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Subject: planning.ResourceSubject, Name: sql.config.Database, Provider: "cloud-sql", ConnectionID: conn.ID, Region: source.Region, ExternalID: source.ConnectionName, PhysicalID: physical, Config: raw}
	if err = planning.NewPgStore(f.db).PutBinding(t.Context(), &binding); err != nil {
		t.Fatal(err)
	}
	sources, err := f.store.ListNativeSources(t.Context(), "org", "project", recovery.NativePageInput{Resource: binding.Name})
	if err != nil || len(sources.Sources) != 1 {
		t.Fatal("source history missing", err)
	}
	sourceID := sources.Sources[0].ID
	accounts := nativeCopyAccounts{connections: map[string]*connections.Connection{conn.ID: conn}, keys: map[string][]byte{}}
	vault := nativeCredentialVault(t, f)
	var mu sync.Mutex
	var copied *gcp.SQLInstance
	var adminPassword string
	writes := map[string]int{}
	operations := map[string]map[string]any{}
	sourceChanged := false
	operation := func(name, kind, target string) map[string]any {
		op := map[string]any{"kind": "sql#operation", "name": name, "status": "DONE", "targetProject": source.Project, "targetId": target, "operationType": kind, "insertTime": now.Add(-time.Minute).Format(time.RFC3339Nano), "endTime": now.Format(time.RFC3339Nano)}
		operations[name] = op
		return op
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.Path, "/v1/projects/source-project/")
		switch {
		case r.URL.Path == "/token":
			fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
		case r.URL.Path == "/v3/projects/source-project:testIamPermissions":
			var input struct {
				Permissions []string `json:"permissions"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&input); decodeErr != nil {
				t.Error(decodeErr)
			}
			_ = json.NewEncoder(w).Encode(input)
		case r.Method == http.MethodGet && (path == "backups" || path == "backups/final-original"):
			if deleted == nil {
				t.Error("live source read retained backups")
				w.WriteHeader(500)
				return
			}
			backup := map[string]any{"kind": "sql#backup", "name": "projects/source-project/backups/final-original", "instance": source.Name, "type": "FINAL", "state": "SUCCESSFUL", "databaseVersion": "POSTGRES_17", "instanceDeletionTime": *deleted, "instanceSettings": source, "backupInterval": map[string]any{"startTime": deleted.Add(-time.Minute), "endTime": *deleted}, "expiryTime": now.Add(24 * time.Hour)}
			if path == "backups" {
				_ = json.NewEncoder(w).Encode(map[string]any{"backups": []any{backup}})
			} else {
				_ = json.NewEncoder(w).Encode(backup)
			}
		case r.Method == http.MethodGet && path == "instances/source-database":
			if deleted != nil {
				w.WriteHeader(404)
				return
			}
			observed := source
			if sourceChanged {
				observed.Settings.Version = "2"
			}
			_ = json.NewEncoder(w).Encode(observed)
		case r.Method == http.MethodGet && path == "instances/source-database/getLatestRecoveryTime":
			if deleted != nil && r.URL.Query().Get("sourceInstanceDeletionTime") != deleted.Format(time.RFC3339Nano) {
				t.Error("deleted log window lost exact deletion time")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#getLatestRecoveryTime", "earliestRecoveryTime": now.Add(-2 * time.Hour), "latestRecoveryTime": now.Add(-time.Minute)})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "operations/"):
			op, ok := operations[strings.TrimPrefix(path, "operations/")]
			if !ok {
				t.Error("uncaptured operation", path)
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(op)
		case r.Method == http.MethodPost && path == "instances/source-database/clone":
			var body struct {
				Context struct {
					Name      string `json:"destinationInstanceName"`
					Time      string `json:"pointInTime"`
					DeletedAt string `json:"sourceInstanceDeletionTime"`
				} `json:"cloneContext"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
				t.Error(decodeErr)
			}
			if deleted != nil && body.Context.DeletedAt != deleted.Format(time.RFC3339Nano) {
				t.Error("deleted clone lost exact deletion time")
			}
			if deleted == nil && body.Context.DeletedAt != "" {
				t.Error("live clone gained deletion context")
			}
			if body.Context.Time != selected.Format(time.RFC3339Nano) {
				t.Error("PITR timestamp changed", body.Context.Time)
			}
			clone := source
			clone.Name = body.Context.Name
			clone.ConnectionName = source.Project + ":" + source.Region + ":" + clone.Name
			clone.CreateTime = now.Add(-30 * time.Second).Format(time.RFC3339Nano)
			copied = &clone
			writes["clone"]++
			op := operation("clone-operation", "CLONE", clone.Name)
			if scenario == "failed-clone" {
				op["error"] = map[string]any{"errors": []any{map[string]string{"message": "clone failed"}}}
			}
			_ = json.NewEncoder(w).Encode(op)
		case copied != nil && r.Method == http.MethodPatch && path == "instances/"+copied.Name:
			var body gcp.SQLInstance
			if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
				t.Error(decodeErr)
			}
			copied.Settings.IPConfiguration = body.Settings.IPConfiguration
			copied.Settings.Backup = body.Settings.Backup
			copied.Settings.UserLabels = body.Settings.UserLabels
			copied.Settings.DeletionProtection = body.Settings.DeletionProtection
			copied.Settings.Version = "2"
			endpoint, _ := json.Marshal(map[string]string{"consumerProject": route.ConsumerProject, "consumerNetwork": route.Network, "status": "ACTIVE", "consumerNetworkStatus": "VALID", "instanceAutoDnsStatus": "AUTO_DNS_OK", "ipAddress": "10.30.0.8"})
			copied.Settings.IPConfiguration.PSC.AutoConnections = []json.RawMessage{endpoint}
			writes["network"]++
			_ = json.NewEncoder(w).Encode(operation("network-operation", "UPDATE", copied.Name))
		case copied != nil && r.Method == http.MethodPost && path == "instances/"+copied.Name+"/users":
			var body struct{ Name, Password string }
			if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
				t.Error(decodeErr)
			}
			if body.Name != "postgres" {
				t.Error("wrong copied admin", body.Name)
			}
			adminPassword = body.Password
			var ddl string
			if queryErr := sql.admin.QueryRow(r.Context(), `SELECT format('CREATE ROLE postgres LOGIN CREATEROLE NOSUPERUSER PASSWORD %L',$1::text)`, body.Password).Scan(&ddl); queryErr != nil {
				t.Error(queryErr)
			}
			if _, execErr := sql.admin.Exec(r.Context(), ddl+`; GRANT pg_read_all_data TO postgres WITH ADMIN OPTION`); execErr != nil {
				t.Error(execErr)
			}
			writes["admin"]++
			_ = json.NewEncoder(w).Encode(operation("admin-operation", "CREATE_USER", copied.Name))
		case copied != nil && r.Method == http.MethodDelete && path == "instances/"+copied.Name:
			if r.URL.Query().Get("enableFinalBackup") != "false" {
				t.Error("copy cleanup requested final backup")
			}
			op := operation("delete-operation", "DELETE", copied.Name)
			copied = nil
			writes["delete"]++
			_ = json.NewEncoder(w).Encode(op)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "instances/"):
			if copied == nil || path != "instances/"+copied.Name {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(copied)
		default:
			t.Errorf("unexpected provider operation %s %s", r.Method, r.URL)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	accounts.keys[conn.ID] = nativeSQLAccountKey(t, source.Project, server.URL+"/token")
	backend := &deployments.NativeDatabases{Google: gcp.New(server.URL), Credentials: accounts, Secrets: vault, SQLConnector: func(context.Context, *gcp.Session, string, string) (gcp.SQLConnector, error) {
		return nativeFixtureConnector{dial: sql.verifier.Dial}, nil
	}}
	if strings.HasPrefix(scenario, "http-cli-dispatch") {
		runCloudSQLPITRHTTP(t, f, sql, binding, cfg, server.URL, selected, deleted, backend.SQLConnector)
		mu.Lock()
		defer mu.Unlock()
		for _, operation := range []string{"clone", "network", "admin", "delete"} {
			if writes[operation] != 1 {
				t.Fatal("HTTP/CLI workflow repeated a provider mutation", writes)
			}
		}
		return
	}
	point, err := f.store.RefreshNativePITR(t.Context(), "org", sourceID, func(ctx context.Context, _ *recovery.NativeSource) (*recovery.NativePoint, error) {
		return backend.NativePITRWindow(ctx, &binding)
	})
	if err != nil {
		t.Fatal("PSC source window unavailable", err)
	}
	point.RestoreTime = &selected
	request := &recovery.NativeRestore{ID: "pitr-copy", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", SourceID: sourceID, PointID: point.ID, Point: *point, RequestID: "pitr-request", CreatedBy: "operator", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Target: recovery.NativeRestoreTarget{Provider: "cloud-sql", ConnectionID: conn.ID, NativeProject: source.Project, AccountIdentity: identity, Region: source.Region, CloudSQLNetwork: route}}
	if _, err = f.store.CreateNativeRestore(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.ClaimNativeRestore(t.Context(), "org", request.ID, "pitr-worker")
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Scope, Stage, Operation string
		CloneOperation          string `json:"clone_operation"`
		DeleteOperation         string `json:"delete_operation"`
		CredentialVersion       int64  `json:"credential_version"`
	}
	t.Cleanup(func() {
		for _, role := range []string{"lp_recovery_" + saved.Scope[:min(32, len(saved.Scope))], "postgres"} {
			if _, execErr := sql.admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize()); execErr != nil {
				t.Error(execErr)
			}
		}
	})
	injected := false
	rejected := errors.New("checkpoint interrupted")
	principal := security.Principal{OrgID: "org", UserID: "operator"}
	control := recovery.NativeCopyControl{NativeWorkerControl: recovery.NativeWorkerControl{Check: func(ctx context.Context) error {
		if (scenario == "network-timeout" && saved.Operation == "network-operation" || scenario == "admin-timeout" && saved.Operation == "admin-operation") && !injected {
			injected = true
			return context.DeadlineExceeded
		}
		return f.store.CheckNativeLease(ctx, claimed, "running")
	}, Save: func(ctx context.Context, raw json.RawMessage) error {
		if decodeErr := json.Unmarshal(raw, &saved); decodeErr != nil {
			return decodeErr
		}
		fail := scenario == "network-restart" && saved.Stage == "admin" || scenario == "lost-clone-operation" && saved.Operation == "clone-operation" || scenario == "lost-network-operation" && saved.Operation == "network-operation" || scenario == "lost-admin-operation" && saved.Operation == "admin-operation"
		if fail && !injected {
			injected = true
			return rejected
		}
		if saveErr := f.store.SaveNativeCheckpoint(ctx, claimed, raw); saveErr != nil {
			return saveErr
		}
		if scenario == "network-drift" && saved.Stage == "admin" {
			mu.Lock()
			copied.Settings.IPConfiguration.PSC.AllowedProjects = []string{"app-project"}
			mu.Unlock()
		}
		return nil
	}}, Selected: func(ctx context.Context) (*recovery.NativePoint, error) {
		return backend.CheckNativeSnapshot(ctx, &claimed.Source, claimed.Point)
	}}
	if scenario == "source-changed" {
		mu.Lock()
		sourceChanged = true
		mu.Unlock()
	}
	result, err := backend.RestoreNative(t.Context(), principal, *claimed, control)
	if scenario == "network-timeout" || scenario == "admin-timeout" {
		if !errors.Is(err, recovery.ErrNativePending) {
			t.Fatal("captured provider timeout was terminal", err)
		}
		if finishErr := f.store.FinishNativeRestore(t.Context(), claimed, result, time.Second, err); finishErr != nil {
			t.Fatal(finishErr)
		}
		resumed, claimErr := f.store.ClaimNativeRestore(t.Context(), "org", claimed.ID, "pitr-worker-resumed")
		if claimErr != nil {
			t.Fatal("timed-out copy was not queued for retry", claimErr)
		}
		claimed = resumed
		result, err = backend.RestoreNative(t.Context(), principal, *claimed, control)
	}
	lostSetup := scenario == "lost-clone-operation" || scenario == "lost-network-operation" || scenario == "lost-admin-operation"
	if scenario == "network-restart" || lostSetup {
		if !errors.Is(err, rejected) {
			t.Fatal("checkpoint interruption ignored", err)
		}
		retained, readErr := f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		result, err = backend.RestoreNative(t.Context(), principal, *retained, control)
		if lostSetup {
			if err == nil || result.SQLVerified {
				t.Fatal("uncaptured mutation was replayed")
			}
			mu.Lock()
			defer mu.Unlock()
			adminWrites := 0
			if scenario == "lost-admin-operation" {
				adminWrites = 1
			}
			if writes["clone"] != 1 || writes["network"] > 1 || writes["admin"] != adminWrites {
				t.Fatal("uncertain operation duplicated", writes)
			}
			return
		}
	}
	if scenario == "source-changed" || scenario == "network-drift" {
		if err == nil || result.SQLVerified {
			t.Fatal("changed source or copy accepted")
		}
		mu.Lock()
		defer mu.Unlock()
		if writes["admin"] != 0 || scenario == "source-changed" && writes["clone"] != 0 {
			t.Fatal("unsafe copy mutated", writes)
		}
		return
	}
	if scenario == "failed-clone" {
		if err == nil {
			t.Fatal("failed clone verified")
		}
	} else if err != nil || !result.SQLVerified {
		t.Fatal("PITR SQL verification failed", result, err)
	}
	if err = f.store.FinishNativeRestore(t.Context(), claimed, result, time.Second, err); err != nil {
		t.Fatal(err)
	}
	stored, err := f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(stored.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.CloneOperation != "clone-operation" || strings.Contains(string(stored.Checkpoint), adminPassword) && adminPassword != "" {
		t.Fatal("clone witness lost or password published")
	}
	if scenario != "failed-clone" {
		if scenario == "credentials-replaced" {
			mu.Lock()
			original := copied.CreateTime
			copied.CreateTime = now.Add(time.Minute).Format(time.RFC3339Nano)
			mu.Unlock()
			creds, accessErr := backend.NativeCredentials(t.Context(), principal, *stored, func(context.Context) error { return nil })
			if accessErr == nil || creds != nil {
				t.Fatal("replacement received the original PITR reader credentials", accessErr)
			}
			mu.Lock()
			copied.CreateTime = original
			mu.Unlock()
		}
		creds, accessErr := backend.NativeCredentials(t.Context(), principal, *stored, func(context.Context) error { return nil })
		if accessErr != nil || creds == nil {
			t.Fatal("PITR reader inaccessible", accessErr)
		}
		serialized, _ := json.Marshal(creds)
		if !strings.Contains(string(serialized), `"private_service_connect":true`) {
			t.Fatal("PITR credentials omit required PSC transport")
		}
		readCfg := sql.config.Copy()
		readCfg.User, readCfg.Password = creds.Username, creds.Password
		reader, readErr := pgx.ConnectConfig(t.Context(), readCfg)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var value string
		readErr = reader.QueryRow(t.Context(), `SELECT value FROM public.recovered_rows WHERE id=7`).Scan(&value)
		_ = reader.Close(t.Context())
		if readErr != nil || value != "retained-after-restore" {
			t.Fatal("reader could not read fixture rows", readErr)
		}
	}
	if _, err = f.store.RequestNativeCleanup(t.Context(), "org", claimed.ID, "operator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cleanup, err := f.store.ClaimNativeCleanup(t.Context(), "org", claimed.ID, "pitr-cleaner")
	if err != nil {
		t.Fatal(err)
	}
	if scenario == "cleanup-replaced" {
		mu.Lock()
		copied.CreateTime = now.Add(time.Minute).Format(time.RFC3339Nano)
		mu.Unlock()
	}
	cleanupInterrupted := false
	cleanupControl := recovery.NativeWorkerControl{Check: func(ctx context.Context) error {
		if scenario == "delete-timeout" && saved.DeleteOperation == "delete-operation" && !cleanupInterrupted {
			cleanupInterrupted = true
			return context.DeadlineExceeded
		}
		return f.store.CheckNativeLease(ctx, cleanup, "deleting")
	}, Save: func(ctx context.Context, raw json.RawMessage) error {
		if decodeErr := json.Unmarshal(raw, &saved); decodeErr != nil {
			return decodeErr
		}
		if scenario == "lost-delete-operation" && saved.DeleteOperation == "delete-operation" && !cleanupInterrupted {
			cleanupInterrupted = true
			return rejected
		}
		return f.store.SaveNativeCheckpoint(ctx, cleanup, raw)
	}}
	removed, err := backend.DeleteNative(t.Context(), principal, *cleanup, cleanupControl)
	if scenario == "delete-timeout" || scenario == "lost-delete-operation" {
		if err == nil || removed.ConfirmedAbsent {
			t.Fatal("interrupted cleanup claimed success", err)
		}
		key := deployments.ResourceKey("org", "environment", "native-copy:"+cleanup.ID, "credentials")
		if _, readErr := vault.GetVersion(t.Context(), key, saved.CredentialVersion); readErr != nil {
			t.Fatal("interrupted cleanup removed its credentials", readErr)
		}
		if finishErr := f.store.FinishNativeCleanup(t.Context(), cleanup, err); finishErr != nil {
			t.Fatal(finishErr)
		}
		if _, requestErr := f.store.RequestNativeCleanup(t.Context(), "org", cleanup.ID, "operator", func(context.Context) error { return nil }); requestErr != nil {
			t.Fatal(requestErr)
		}
		cleanup, err = f.store.ClaimNativeCleanup(t.Context(), "org", cleanup.ID, "pitr-cleaner-resumed")
		if err != nil {
			t.Fatal(err)
		}
		removed, err = backend.DeleteNative(t.Context(), principal, *cleanup, cleanupControl)
		if scenario == "lost-delete-operation" {
			if err == nil || removed.ConfirmedAbsent {
				t.Fatal("uncaptured delete was silently adopted", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if writes["delete"] != 1 {
				t.Fatal("uncertain delete replayed", writes)
			}
			if _, readErr := vault.GetVersion(t.Context(), key, saved.CredentialVersion); readErr != nil {
				t.Fatal("uncertain delete retry discarded credentials", readErr)
			}
			return
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if scenario == "cleanup-replaced" {
		if err == nil || writes["delete"] != 0 || removed.ConfirmedAbsent {
			t.Fatal("replacement deleted", err, writes)
		}
		return
	}
	if err != nil || !removed.ConfirmedAbsent || writes["clone"] != 1 || writes["delete"] != 1 {
		t.Fatal("PITR cleanup failed", removed, err, writes)
	}
	if scenario != "failed-clone" && (writes["network"] != 1 || writes["admin"] != 1) {
		t.Fatal("copy setup replayed", writes)
	}
	key := deployments.ResourceKey("org", "environment", "native-copy:"+cleanup.ID, "credentials")
	if _, readErr := vault.GetVersion(t.Context(), key, saved.CredentialVersion); readErr == nil {
		t.Fatal("confirmed absence retained readable credentials")
	}
	if err = f.store.FinishNativeCleanup(t.Context(), cleanup, nil); err != nil {
		t.Fatal(err)
	}
	removedRecord, readErr := f.store.GetNativeRestore(t.Context(), "org", cleanup.ID)
	if readErr != nil || removedRecord.Status != "deleted" {
		t.Fatal("confirmed cleanup did not persist deleted status", readErr)
	}
}

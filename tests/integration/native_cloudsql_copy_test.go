//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

type nativeCopyAccounts struct {
	connections map[string]*connections.Connection
	keys        map[string][]byte
}

func nativeSQLAccountKey(t *testing.T, project, token string) []byte {
	t.Helper()
	var fields map[string]string
	if err := json.Unmarshal([]byte(serviceAccountKey(t, token)), &fields); err != nil {
		t.Fatal(err)
	}
	fields["project_id"], fields["client_email"] = project, "recovery@"+project+".iam.gserviceaccount.com"
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (a nativeCopyAccounts) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	c := a.connections[id]
	if c == nil || c.OrgID != org {
		return nil, nil, recovery.ErrNotFound
	}
	return c, a.keys[id], nil
}

type nativeFixtureConnector struct {
	dial func(context.Context, string, string) (net.Conn, error)
}

type nativeSecretDeleteFault struct {
	deployments.NativeCopySecrets
	failed bool
	err    error
}

func (s *nativeSecretDeleteFault) Delete(ctx context.Context, key string) error {
	if !s.failed {
		s.failed = true
		return s.err
	}
	return s.NativeCopySecrets.Delete(ctx, key)
}

func (c nativeFixtureConnector) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return c.dial(ctx, network, address)
}
func (nativeFixtureConnector) Close() error { return nil }

func TestNativeCloudSQLCopyBackendKeepsCredentialsAndCleansPinnedCopy(t *testing.T) {
	for _, scenario := range []string{"complete", "denied", "credential-save-failure", "captured-restart", "proof-save-failure", "lost-restore-operation", "lost-admin-operation", "replaced-before-admin", "network-before-admin", "revoked-before-admin", "target-account-changed", "source-point-changed", "token-lost-before-admin", "cleanup-denied", "cleanup-replaced", "cleanup-capture-failure", "secret-delete-failure", "failed-restore-absent", "failed-restore-partial", "failed-restore-replacement", "admin-error-redaction"} {
		t.Run(scenario, func(t *testing.T) { runNativeCloudSQLCopyBackend(t, scenario) })
	}
}

func runNativeCloudSQLCopyBackend(t *testing.T, scenario string) {
	t.Helper()
	// The control-plane store is on the shared test server. SQL writes and
	// cluster-wide test roles stay on the dedicated native PostgreSQL instance.
	f := nativePointHistoryFixture(t)
	sql := nativeSQLFixtureFor(t)
	profile, err := manageddb.Postgres("cloud-sql", "17")
	if err != nil {
		t.Fatal(err)
	}
	accounts := nativeCopyAccounts{connections: map[string]*connections.Connection{}, keys: map[string][]byte{}}
	for id, project := range map[string]string{"source-account": "source-project", "copy-account": "recovery-project"} {
		c := &connections.Connection{ID: id, OrgID: "org", Cloud: connections.GCP, Name: id, Account: project, Details: map[string]string{"project_id": project}, Status: connections.StatusReady, CreatedBy: "operator", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err = connections.NewPgStore(f.db).CreateConnection(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		accounts.connections[id] = c
	}
	source := gcp.SQLInstance{Name: "source-database", Project: "source-project", Region: "us-west1", ConnectionName: "source-project:us-west1:source-database", CreateTime: "2026-10-21T00:00:00Z", DatabaseVersion: "POSTGRES_17"}
	pin, err := source.Incarnation(source.Project, source.Region, source.Name)
	if err != nil {
		t.Fatal(err)
	}
	cfg := planning.ResourceConfig{Provider: "cloud-sql", Kind: "postgres", Version: "17", Region: source.Region, Managed: &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: "source-account", AccountIdentity: connections.AccountIdentity(connections.GCP, "source-project", accounts.connections["source-account"].Details)}}
	raw, _ := json.Marshal(cfg)
	binding := &planning.Binding{ID: "native-copy-source", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Subject: planning.ResourceSubject, Name: sql.config.Database, Provider: "cloud-sql", ConnectionID: "source-account", Region: source.Region, ExternalID: source.ConnectionName, PhysicalID: pin, Config: raw, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = planning.NewPgStore(f.db).PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	sources, err := f.store.ListNativeSources(t.Context(), "org", "project", recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range sources.Sources {
		if item.Provider == "cloud-sql" {
			f.source = item
		}
	}
	point := recovery.NativePoint{ProviderID: "projects/source-project/backups/original-backup", Name: "original-backup", Kind: "ON_DEMAND", Status: "SUCCESSFUL", Engine: "postgres", Version: "17", Encrypted: true, CreatedAt: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)}
	if err = f.refresh(t.Context(), []recovery.NativePoint{point}); err != nil {
		t.Fatal(err)
	}
	points, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(points.Points) != 1 {
		t.Fatal("missing Cloud SQL point", err)
	}
	request := &recovery.NativeRestore{ID: "sql-copy-one", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", SourceID: f.source.ID, PointID: points.Points[0].ID, Point: points.Points[0], RequestID: "copy-request", CreatedBy: "operator", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Target: recovery.NativeRestoreTarget{Provider: "cloud-sql", ConnectionID: "copy-account", NativeProject: "recovery-project", AccountIdentity: connections.AccountIdentity(connections.GCP, "recovery-project", accounts.connections["copy-account"].Details), Region: "europe-west1"}}
	if _, err = f.store.CreateNativeRestore(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.ClaimNativeRestore(t.Context(), "org", request.ID, "copy-worker")
	if err != nil {
		t.Fatal(err)
	}
	vault := nativeCredentialVault(t, f)
	var mu sync.Mutex
	var copyName, intent string
	exists := false
	writes := map[string]int{}
	var adminPassword string
	var unsafeCopy string
	operation := func(name, kind string) map[string]any {
		value := map[string]any{"kind": "sql#operation", "name": name, "status": "DONE", "targetProject": "recovery-project", "targetId": copyName, "operationType": kind, "insertTime": "2026-10-22T00:59:50Z", "endTime": "2026-10-22T01:00:30Z"}
		if name == "restore-operation" && strings.HasPrefix(scenario, "failed-restore-") {
			value["error"] = map[string]any{"errors": []any{map[string]string{"message": "restore failed"}}}
		}
		return value
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/projects/source-project/backups/original-backup" {
			created := point.CreatedAt
			if scenario == "source-point-changed" {
				created = created.Add(time.Second)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#backup", "name": point.ProviderID, "instance": source.Name, "type": point.Kind, "state": point.Status, "databaseVersion": "POSTGRES_17", "backupInterval": map[string]string{"startTime": created.Format(time.RFC3339Nano), "endTime": point.CreatedAt.Add(time.Minute).Format(time.RFC3339Nano)}, "instanceSettings": source})
			return
		}
		if strings.Contains(r.URL.Path, "/operations/") {
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			kind := map[string]string{"restore-operation": "RESTORE_VOLUME", "admin-operation": "CREATE_USER", "delete-operation": "DELETE"}[name]
			_ = json.NewEncoder(w).Encode(operation(name, kind))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/restoreBackup") {
			if !strings.HasPrefix(r.URL.Path, "/v1/projects/recovery-project/instances/") {
				t.Error("restore wrote to the original project")
			}
			var body struct {
				Settings struct {
					Name     string `json:"name"`
					Settings struct {
						Labels map[string]string `json:"userLabels"`
					} `json:"settings"`
				} `json:"restoreInstanceSettings"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
				t.Error(decodeErr)
			}
			copyName, intent = body.Settings.Name, body.Settings.Settings.Labels[gcp.SQLIntentLabel]
			exists = scenario != "failed-restore-absent"
			writes["restore"]++
			_ = json.NewEncoder(w).Encode(operation("restore-operation", "RESTORE_VOLUME"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/users") {
			if r.URL.Path != "/v1/projects/recovery-project/instances/"+copyName+"/users" {
				t.Error("copy setup changed another instance's credentials")
			}
			var body struct{ Name, Password string }
			if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
				t.Error(decodeErr)
			}
			if body.Name != "postgres" || len(body.Password) < 40 {
				t.Error("copy admin credentials missing")
			}
			adminPassword = body.Password
			writes["admin"]++
			var ddl string
			if queryErr := sql.admin.QueryRow(r.Context(), `SELECT format('CREATE ROLE postgres LOGIN CREATEROLE NOSUPERUSER PASSWORD %L',$1::text)`, body.Password).Scan(&ddl); queryErr != nil {
				t.Error(queryErr)
			}
			if _, execErr := sql.admin.Exec(r.Context(), ddl+`; GRANT pg_read_all_data TO postgres WITH ADMIN OPTION`); execErr != nil {
				t.Error(execErr)
			}
			if scenario == "admin-error-redaction" {
				value := operation("admin-operation", "CREATE_USER")
				value["error"] = map[string]any{"errors": []any{map[string]string{"message": "rejected credential " + body.Password}}}
				_ = json.NewEncoder(w).Encode(value)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"kind": "sql#operation", "name": "admin-operation", "status": "DONE", "targetProject": "recovery-project", "targetId": copyName, "operationType": "CREATE_USER"})
			return
		}
		if r.Method == http.MethodDelete {
			if r.URL.Path != "/v1/projects/recovery-project/instances/"+copyName {
				t.Error("copy cleanup deleted another instance")
			}
			if r.URL.Query().Get("enableFinalBackup") != "false" {
				t.Error("temporary copy requested a final backup")
			}
			writes["delete"]++
			exists = false
			_ = json.NewEncoder(w).Encode(map[string]string{"kind": "sql#operation", "name": "delete-operation", "status": "DONE", "targetProject": "recovery-project", "targetId": copyName, "operationType": "DELETE"})
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/instances/") {
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"message":"missing"}}`)
				return
			}
			created := "2026-10-22T01:00:00Z"
			network := map[string]any{"ipv4Enabled": true, "authorizedNetworks": []any{}}
			if unsafeCopy == "replaced-before-admin" || unsafeCopy == "cleanup-replaced" || scenario == "failed-restore-replacement" {
				created = "2026-10-22T02:00:00Z"
			}
			if unsafeCopy == "network-before-admin" {
				network["authorizedNetworks"] = []any{map[string]string{"value": "0.0.0.0/0"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#instance", "name": copyName, "project": "recovery-project", "region": "europe-west1", "connectionName": "recovery-project:europe-west1:" + copyName, "createTime": created, "databaseVersion": "POSTGRES_17", "state": "RUNNABLE", "settings": map[string]any{"tier": profile.ComputeClass, "dataDiskSizeGb": "10", "dataDiskType": "PD_SSD", "storageAutoResize": false, "connectorEnforcement": "REQUIRED", "ipConfiguration": network, "userLabels": map[string]string{gcp.SQLIntentLabel: intent, "managed-by": "loomplane"}}})
			return
		}
		t.Errorf("unexpected native request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	for id, c := range accounts.connections {
		accounts.keys[id] = nativeSQLAccountKey(t, c.Details["project_id"], server.URL+"/token")
	}
	backend := &deployments.NativeDatabases{Google: gcp.New(server.URL), Credentials: accounts, Secrets: vault, SQLConnector: func(context.Context, *gcp.Session, string, string) (gcp.SQLConnector, error) {
		return nativeFixtureConnector{dial: sql.verifier.Dial}, nil
	}}
	copyBackend, ok := any(backend).(recovery.NativeCopyBackend)
	if !ok {
		t.Fatal("native Cloud SQL copies have no execution backend")
	}
	principal := security.Principal{OrgID: "org", UserID: "operator"}
	if scenario == "target-account-changed" {
		accounts.connections["copy-account"].Details["project_id"] = "changed-project"
	}
	rejected := errors.New("checkpoint rejected")
	injected, revoked, selected := false, false, 0
	t.Cleanup(func() {
		stored, readErr := f.store.GetNativeRestore(context.Background(), "org", claimed.ID)
		if readErr != nil {
			t.Error(readErr)
			return
		}
		var metadata struct {
			Scope string `json:"scope"`
		}
		if json.Unmarshal(stored.Checkpoint, &metadata) != nil || len(metadata.Scope) != 64 {
			return
		}
		for _, role := range []string{"lp_recovery_" + metadata.Scope[:32], "postgres"} {
			if _, dropErr := sql.admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize()); dropErr != nil {
				t.Error(dropErr)
			}
		}
	})
	control := recovery.NativeCopyControl{NativeWorkerControl: recovery.NativeWorkerControl{Check: func(ctx context.Context) error {
		if scenario == "denied" || revoked {
			return security.ErrForbidden
		}
		return f.store.CheckNativeLease(ctx, claimed, "running")
	}, Save: func(ctx context.Context, state json.RawMessage) error {
		var metadata struct {
			Stage, Operation  string
			CredentialVersion int64 `json:"credential_version"`
		}
		if decodeErr := json.Unmarshal(state, &metadata); decodeErr != nil {
			return decodeErr
		}
		fail := scenario == "credential-save-failure" && metadata.CredentialVersion > 0 || scenario == "captured-restart" && metadata.Stage == "admin" && metadata.Operation == "" || scenario == "proof-save-failure" && metadata.Stage == "ready" || scenario == "lost-restore-operation" && metadata.Operation == "restore-operation" || scenario == "lost-admin-operation" && metadata.Operation == "admin-operation"
		if !injected && fail {
			injected = true
			return rejected
		}
		if saveErr := f.store.SaveNativeCheckpoint(ctx, claimed, state); saveErr != nil {
			return saveErr
		}
		if metadata.Stage == "admin" && metadata.Operation == "" {
			mu.Lock()
			if scenario == "replaced-before-admin" || scenario == "network-before-admin" {
				unsafeCopy = scenario
			}
			mu.Unlock()
			if scenario == "revoked-before-admin" {
				revoked = true
			}
			if scenario == "token-lost-before-admin" {
				_, tokenErr := f.sql.Exec(ctx, `UPDATE loomplane_native_restores SET token='replacement-worker' WHERE id=$1`, claimed.ID)
				return tokenErr
			}
		}
		return nil
	}}, Selected: func(ctx context.Context) (*recovery.NativePoint, error) {
		selected++
		if selected > 1 && scenario == "captured-restart" {
			return nil, recovery.ErrNativeUnavailable
		}
		return backend.CheckNativeSnapshot(ctx, &claimed.Source, claimed.Point)
	}}
	result, err := copyBackend.RestoreNative(t.Context(), principal, *claimed, control)
	if scenario == "admin-error-redaction" {
		if err == nil || result.SQLVerified {
			t.Fatal("failed admin update succeeded")
		}
		if strings.Contains(err.Error(), adminPassword) {
			t.Error("native API diagnostics exposed the copy password")
		}
		if finishErr := f.store.FinishNativeRestore(t.Context(), claimed, result, time.Second, err); finishErr != nil {
			t.Fatal(finishErr)
		}
		stored, readErr := f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
		if readErr != nil || strings.Contains(stored.Error, adminPassword) {
			t.Fatal("native API diagnostics persisted a plaintext password", readErr)
		}
		return
	}
	if strings.HasPrefix(scenario, "failed-restore-") {
		if err == nil || result.SQLVerified {
			t.Fatal("failed native restore succeeded", result, err)
		}
		if finishErr := f.store.FinishNativeRestore(t.Context(), claimed, result, time.Second, err); finishErr != nil {
			t.Fatal(finishErr)
		}
		if _, queueErr := f.store.RequestNativeCleanup(t.Context(), "org", claimed.ID, "operator", func(context.Context) error { return nil }); queueErr != nil {
			t.Fatal(queueErr)
		}
		cleanup, claimErr := f.store.ClaimNativeCleanup(t.Context(), "org", claimed.ID, "failed-copy-cleanup")
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		removed, cleanupErr := backend.DeleteNative(t.Context(), principal, *cleanup, recovery.NativeWorkerControl{Check: func(ctx context.Context) error { return f.store.CheckNativeLease(ctx, cleanup, "deleting") }, Save: func(ctx context.Context, raw json.RawMessage) error {
			return f.store.SaveNativeCheckpoint(ctx, cleanup, raw)
		}})
		mu.Lock()
		deletes, restores, admins := writes["delete"], writes["restore"], writes["admin"]
		mu.Unlock()
		if restores != 1 || admins != 0 {
			t.Fatal("failed-copy cleanup replayed setup", restores, admins)
		}
		if scenario == "failed-restore-replacement" {
			if cleanupErr == nil || removed.ConfirmedAbsent || deletes != 0 {
				t.Fatal("cleanup adopted a replacement after the failed restore", removed, cleanupErr, deletes)
			}
			return
		}
		if cleanupErr != nil || !removed.ConfirmedAbsent {
			t.Fatal("failed restore could not clean its absent or partial copy", removed, cleanupErr)
		}
		if scenario == "failed-restore-absent" && deletes != 0 || scenario == "failed-restore-partial" && deletes != 1 {
			t.Fatal("failed restore cleanup did not respect observed absence", deletes)
		}
		if _, readErr := vault.Get(t.Context(), deployments.ResourceKey("org", "environment", "native-copy:"+claimed.ID, "credentials")); !errors.Is(readErr, deployments.ErrNoCredential) {
			t.Fatal("failed restore cleanup retained its credentials", readErr)
		}
		return
	}
	if scenario == "denied" || scenario == "credential-save-failure" || scenario == "replaced-before-admin" || scenario == "network-before-admin" || scenario == "revoked-before-admin" || scenario == "target-account-changed" || scenario == "source-point-changed" || scenario == "token-lost-before-admin" {
		if err == nil || result.SQLVerified {
			t.Fatal("unsafe native copy reached a SQL proof", result, err)
		}
		mu.Lock()
		restoreWrites, adminWrites := writes["restore"], writes["admin"]
		mu.Unlock()
		if adminWrites != 0 || (scenario == "denied" || scenario == "credential-save-failure" || scenario == "target-account-changed" || scenario == "source-point-changed") && restoreWrites != 0 {
			t.Fatal("denied or unsafe copy issued a native mutation", restoreWrites, adminWrites)
		}
		return
	}
	if scenario == "captured-restart" || scenario == "proof-save-failure" || scenario == "lost-restore-operation" || scenario == "lost-admin-operation" {
		if !errors.Is(err, rejected) || result.SQLVerified {
			t.Fatal("checkpoint failure was ignored", err)
		}
		retained, readErr := f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if scenario != "captured-restart" && scenario != "proof-save-failure" {
			if _, retryErr := copyBackend.RestoreNative(t.Context(), principal, *retained, control); retryErr == nil {
				t.Fatal("an uncaptured mutation was retried without reconciliation")
			}
			var metadata map[string]any
			if decodeErr := json.Unmarshal(retained.Checkpoint, &metadata); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			operation := "restore-operation"
			if scenario == "lost-admin-operation" {
				operation = "admin-operation"
			}
			metadata["operation"] = operation
			retained.Checkpoint, _ = json.Marshal(metadata)
			if saveErr := f.store.SaveNativeCheckpoint(t.Context(), claimed, retained.Checkpoint); saveErr != nil {
				t.Fatal(saveErr)
			}
		}
		restarted := *backend
		result, err = restarted.RestoreNative(t.Context(), principal, *retained, control)
	}
	if err != nil || !result.SQLVerified || result.PhysicalID == "" || result.ExternalID == "" {
		t.Fatal("native restore did not reach real SQL verification", result, err)
	}
	saved, err := f.store.GetNativeRestore(t.Context(), "org", claimed.ID)
	if err != nil || strings.Contains(string(saved.Checkpoint), adminPassword) {
		t.Fatal("copy checkpoint lost state or leaked credentials", err)
	}
	var state struct {
		Scope             string `json:"scope"`
		CredentialVersion int64  `json:"credential_version"`
	}
	if err = json.Unmarshal(saved.Checkpoint, &state); err != nil || state.CredentialVersion < 1 {
		t.Fatal("copy did not checkpoint its exact Vault version", err)
	}
	key := deployments.ResourceKey("org", "environment", "native-copy:"+claimed.ID, "credentials")
	kept, err := vault.GetVersion(t.Context(), key, state.CredentialVersion)
	if err != nil {
		t.Fatal(err)
	}
	var credentials struct {
		AdminPassword  string `json:"admin_password"`
		ReaderPassword string `json:"reader_password"`
	}
	if err = json.Unmarshal(kept, &credentials); err != nil || credentials.AdminPassword != adminPassword || credentials.ReaderPassword == "" {
		t.Fatal("copy credentials were not retained", err)
	}
	readerName := "lp_recovery_" + state.Scope[:32]
	readCfg := sql.config.Copy()
	readCfg.User, readCfg.Password = readerName, credentials.ReaderPassword
	reader, err := pgx.ConnectConfig(t.Context(), readCfg)
	if err != nil {
		t.Fatal(err)
	}
	var retained string
	err = reader.QueryRow(t.Context(), `SELECT value FROM public.recovered_rows WHERE id=7`).Scan(&retained)
	_ = reader.Close(t.Context())
	if err != nil || retained != "retained-after-restore" {
		t.Fatal("copy reader cannot read retained data", err)
	}
	if _, err = vault.PutNativeVersion(t.Context(), *claimed, []byte(`{"admin_password":"unrelated-late-writer"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = copyBackend.RestoreNative(t.Context(), principal, *saved, control); err != nil {
		t.Fatal("restart read the latest credential instead of its retained version", err)
	}
	mu.Lock()
	restoreWrites, adminWrites := writes["restore"], writes["admin"]
	mu.Unlock()
	if restoreWrites != 1 || adminWrites != 1 {
		t.Fatal("resume repeated a native mutation", restoreWrites, adminWrites)
	}
	if err = f.store.FinishNativeRestore(t.Context(), claimed, result, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RequestNativeCleanup(t.Context(), "org", claimed.ID, "operator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cleanup, err := f.store.ClaimNativeCleanup(t.Context(), "org", claimed.ID, "cleanup-worker")
	if err != nil {
		t.Fatal(err)
	}
	cleanupBackend, ok := any(backend).(recovery.NativeCleanupBackend)
	if !ok {
		t.Fatal("native Cloud SQL copies have no cleanup backend")
	}
	if scenario == "cleanup-replaced" {
		mu.Lock()
		unsafeCopy = scenario
		mu.Unlock()
	}
	if scenario == "secret-delete-failure" {
		backend.Secrets = &nativeSecretDeleteFault{NativeCopySecrets: vault, err: rejected}
	}
	cleanupInjected := false
	cleanupControl := recovery.NativeWorkerControl{Check: func(ctx context.Context) error {
		if scenario == "cleanup-denied" {
			return security.ErrForbidden
		}
		return f.store.CheckNativeLease(ctx, cleanup, "deleting")
	}, Save: func(ctx context.Context, raw json.RawMessage) error {
		var metadata struct {
			DeleteOperation string `json:"delete_operation"`
		}
		if decodeErr := json.Unmarshal(raw, &metadata); decodeErr != nil {
			return decodeErr
		}
		if scenario == "cleanup-capture-failure" && !cleanupInjected && metadata.DeleteOperation != "" {
			cleanupInjected = true
			return rejected
		}
		return f.store.SaveNativeCheckpoint(ctx, cleanup, raw)
	}}
	removed, err := cleanupBackend.DeleteNative(t.Context(), principal, *cleanup, cleanupControl)
	if scenario == "cleanup-denied" || scenario == "cleanup-replaced" {
		if err == nil || removed.ConfirmedAbsent {
			t.Fatal("unsafe copy deletion was accepted", removed, err)
		}
		mu.Lock()
		deleteWrites := writes["delete"]
		mu.Unlock()
		if deleteWrites != 0 {
			t.Fatal("unsafe cleanup issued a native deletion")
		}
		if _, readErr := vault.GetVersion(t.Context(), key, state.CredentialVersion); readErr != nil {
			t.Fatal("unsafe cleanup removed credentials", readErr)
		}
		return
	}
	if scenario == "cleanup-capture-failure" || scenario == "secret-delete-failure" {
		if !errors.Is(err, rejected) || removed.ConfirmedAbsent {
			t.Fatal("cleanup failure was reported as complete", removed, err)
		}
		if _, readErr := vault.GetVersion(t.Context(), key, state.CredentialVersion); readErr != nil {
			t.Fatal("failed cleanup lost credentials before its durable boundary", readErr)
		}
		retained, readErr := f.store.GetNativeRestore(t.Context(), "org", cleanup.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if scenario == "cleanup-capture-failure" {
			if _, retryErr := cleanupBackend.DeleteNative(t.Context(), principal, *retained, cleanupControl); retryErr == nil {
				t.Fatal("uncaptured native deletion was retried")
			}
			var metadata map[string]any
			if decodeErr := json.Unmarshal(retained.Checkpoint, &metadata); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			metadata["delete_operation"] = "delete-operation"
			retained.Checkpoint, _ = json.Marshal(metadata)
			if saveErr := f.store.SaveNativeCheckpoint(t.Context(), cleanup, retained.Checkpoint); saveErr != nil {
				t.Fatal(saveErr)
			}
		}
		removed, err = cleanupBackend.DeleteNative(t.Context(), principal, *retained, cleanupControl)
	}
	if err != nil || !removed.ConfirmedAbsent {
		t.Fatal("copy removal was not confirmed", removed, err)
	}
	if _, err = vault.GetVersion(t.Context(), key, state.CredentialVersion); err == nil {
		t.Fatal("confirmed removal retained its readable credentials")
	}
	mu.Lock()
	deleteWrites := writes["delete"]
	mu.Unlock()
	if deleteWrites != 1 {
		t.Fatal("cleanup repeated its delete", deleteWrites)
	}
}

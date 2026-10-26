package deployments

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nativepg"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

var errNativeReconcile = errors.New("native recovery has an uncertain request without its captured operation; reconcile the original request before retrying")

// Native API diagnostics can echo a submitted password. Preserve the cause
// for errors.Is without exposing its message through worker/API diagnostics.
type nativeCredentialMutationError struct{ cause error }

func (e nativeCredentialMutationError) Error() string {
	return "native recovery copy credential update failed"
}
func (e nativeCredentialMutationError) Unwrap() error { return e.cause }

type nativeSQLCopyState struct {
	Version           int    `json:"native_sql_copy"`
	Scope             string `json:"scope"`
	Stage             string `json:"stage"`
	CredentialVersion int64  `json:"credential_version"`
	PhysicalID        string `json:"physical_id,omitempty"`
	Operation         string `json:"operation,omitempty"`
	CloneOperation    string `json:"clone_operation,omitempty"`
	Uncertain         bool   `json:"uncertain,omitempty"`
	DeleteOperation   string `json:"delete_operation,omitempty"`
	DeleteUncertain   bool   `json:"delete_uncertain,omitempty"`
	Deleted           bool   `json:"deleted,omitempty"`
}

// This value is encoded only for encrypted Vault storage, never a checkpoint.
type nativeCopyPasswords struct {
	Scope          string `json:"scope"`
	AdminPassword  string `json:"admin_password"`
	ReaderPassword string `json:"reader_password"`
}

func nativeSQLCopyScope(r recovery.NativeRestore) (string, *planning.ResourceConfig, error) {
	if r.ID == "" || r.OrgID == "" || r.ProjectID == "" || r.EnvironmentID == "" || r.SourceID == "" || r.PointID == "" || r.Source.OrgID != r.OrgID || r.Source.ProjectID != r.ProjectID || r.Source.EnvironmentID != r.EnvironmentID || r.Source.Subject != planning.ResourceSubject || (r.Source.Provider != "cloud-sql" && r.Source.Provider != "rds") || r.Target.Provider != r.Source.Provider || r.Source.PhysicalID == "" || r.Source.ConnectionID == "" || r.Point.ID != r.PointID || r.Point.SourceID != r.SourceID || r.Point.Engine != "postgres" || r.Point.CreatedAt.IsZero() || r.ExpiresAt.IsZero() {
		return "", nil, recovery.ErrInvalid
	}
	for _, segment := range []string{r.OrgID, r.EnvironmentID, r.ID} {
		if strings.ContainsAny(segment, "/\\\x00") {
			return "", nil, recovery.ErrInvalid
		}
	}
	cfg, err := recovery.NativeSQLCopyConfig(r.Source, r.Point, r.Target)
	if err != nil {
		return "", nil, err
	}
	if r.Source.Provider == "cloud-sql" && r.Point.Kind != "pitr" {
		parts := strings.Split(r.Point.ProviderID, "/")
		if _, _, err = boundSQLInstance(r.Source.ExternalID, parts[1], r.Source.Region); err != nil {
			return "", nil, err
		}
	}

	// Scope contains the immutable retained inputs, not worker tokens/status or
	// mutable inventory observations. The same request always names one copy.
	raw, err := json.Marshal(struct {
		Org, Project, Environment, ID, SourceID, PointID, SourceName, SourceExternal, SourcePhysical, SourceConnection string
		Config                                                                                                         json.RawMessage
		Target                                                                                                         recovery.NativeRestoreTarget
		PointReference, PointKind, PointVersion, PointCreated, KMS, Expires                                            string
		PITR                                                                                                           *recovery.NativePITRBounds `json:"pitr,omitempty"`
		RestoreTime                                                                                                    *time.Time                 `json:"restore_time,omitempty"`
	}{r.OrgID, r.ProjectID, r.EnvironmentID, r.ID, r.SourceID, r.PointID, r.Source.Name, r.Source.ExternalID, r.Source.PhysicalID, r.Source.ConnectionID, r.Source.Config, r.Target, r.Point.ProviderID, r.Point.Kind, r.Point.Version, r.Point.CreatedAt.UTC().Format(time.RFC3339Nano), r.Point.KMSKey, r.ExpiresAt.UTC().Format(time.RFC3339Nano), r.Point.PITR, r.Point.RestoreTime})
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), cfg, nil
}

func readNativeSQLCopy(r recovery.NativeRestore) (*nativeSQLCopyState, *planning.ResourceConfig, error) {
	scope, cfg, err := nativeSQLCopyScope(r)
	if err != nil {
		return nil, nil, err
	}
	s := &nativeSQLCopyState{Version: 1, Scope: scope, Stage: "restore"}
	if r.Source.Provider == "cloud-sql" && r.Point.Kind == "pitr" {
		s.Version = 2
	}
	version := s.Version
	raw := bytes.TrimSpace(r.Checkpoint)
	if len(raw) == 0 || bytes.Equal(raw, []byte("{}")) {
		return s, cfg, nil
	}
	if checkErr := decodeDatabaseCheckpoint(raw, s); checkErr != nil {
		return nil, nil, checkErr
	}
	if s.Version != version || s.Scope != scope || !slices.Contains([]string{"restore", "network", "admin", "verify", "ready"}, s.Stage) || s.CredentialVersion < 0 || (!s.Uncertain && s.Operation != "") || (!s.DeleteUncertain && s.DeleteOperation != "") || s.Stage != "restore" && s.PhysicalID == "" || s.CredentialVersion == 0 && (s.Uncertain || s.Stage != "restore") || s.Deleted && s.DeleteUncertain {
		return nil, nil, recovery.ErrConflict
	}
	if version == 1 && (s.Stage == "network" || s.CloneOperation != "") || version == 2 && (s.Stage == "restore" && s.Operation != s.CloneOperation || s.Stage != "restore" && s.CloneOperation == "") {
		return nil, nil, recovery.ErrConflict
	}
	return s, cfg, nil
}

func (s *nativeSQLCopyState) save(ctx context.Context, c recovery.NativeWorkerControl) error {
	if err := c.Check(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return c.Save(ctx, raw)
}

func (s *nativeSQLCopyState) track(ctx context.Context, c recovery.NativeWorkerControl, guard func(context.Context) error, removal bool) gcp.SQLMutation {
	op := s.Operation
	if removal {
		op = s.DeleteOperation
	}
	return gcp.SQLMutation{Resume: op, Check: guard, Before: func() error {
		if removal {
			s.DeleteUncertain = true
		} else {
			s.Uncertain = true
		}
		return s.save(ctx, c)
	}, Started: func(operation string) error {
		if removal {
			s.DeleteOperation = operation
		} else {
			s.Operation = operation
			if s.Version == 2 && s.Stage == "restore" {
				s.CloneOperation = operation
			}
		}
		return s.save(ctx, c)
	}}
}

func (n *NativeDatabases) nativeCopySession(ctx context.Context, r recovery.NativeRestore) (*gcp.Session, error) {
	if n == nil || n.Google == nil || n.Credentials == nil || n.Secrets == nil || r.Target.ConnectionID == "" || r.Target.NativeProject == "" || r.Target.Region == "" || r.Target.AccountIdentity == "" {
		return nil, recovery.ErrInvalid
	}
	c, keyBytes, err := n.Credentials.Credential(ctx, r.OrgID, r.Target.ConnectionID)
	if err != nil {
		return nil, err
	}
	if c == nil || c.ID != r.Target.ConnectionID || c.OrgID != r.OrgID || c.Cloud != connections.GCP || c.Status != connections.StatusReady || c.Details["project_id"] != r.Target.NativeProject || connections.AccountIdentity(c.Cloud, c.Account, c.Details) != r.Target.AccountIdentity {
		return nil, recovery.ErrConflict
	}
	key, err := gcp.ParseKey(keyBytes)
	if err != nil {
		return nil, err
	}
	if key.ProjectID != r.Target.NativeProject {
		return nil, recovery.ErrConflict
	}
	return n.Google.Session(ctx, key)
}

func nativeCopySecretKey(r recovery.NativeRestore) string {
	return ResourceKey(r.OrgID, r.EnvironmentID, "native-copy:"+r.ID, "credentials")
}
func (n *NativeDatabases) copyPasswords(ctx context.Context, r recovery.NativeRestore, s *nativeSQLCopyState, c recovery.NativeWorkerControl) (nativeCopyPasswords, error) {
	if err := c.Check(ctx); err != nil {
		return nativeCopyPasswords{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if s.CredentialVersion == 0 {
		passwords := nativeCopyPasswords{Scope: s.Scope, AdminPassword: rand.Text() + rand.Text(), ReaderPassword: rand.Text() + rand.Text()}
		raw, err := json.Marshal(passwords)
		if err != nil {
			return nativeCopyPasswords{}, err
		}
		version, err := n.Secrets.PutNativeVersion(ctx, r, raw)
		if err != nil {
			return nativeCopyPasswords{}, err
		}
		s.CredentialVersion = version
		if checkErr := s.save(ctx, c); checkErr != nil {
			return nativeCopyPasswords{}, checkErr
		}
	}
	passwords, err := n.readCopyPasswords(ctx, r, s)
	if err != nil {
		return nativeCopyPasswords{}, err
	}
	return passwords, c.Check(ctx)
}

func (n *NativeDatabases) readCopyPasswords(ctx context.Context, r recovery.NativeRestore, s *nativeSQLCopyState) (nativeCopyPasswords, error) {
	if s.CredentialVersion <= 0 {
		return nativeCopyPasswords{}, recovery.ErrConflict
	}
	raw, err := n.Secrets.GetVersion(ctx, nativeCopySecretKey(r), s.CredentialVersion)
	if err != nil {
		return nativeCopyPasswords{}, err
	}
	var p nativeCopyPasswords
	if json.Unmarshal(raw, &p) != nil || p.Scope != s.Scope || len(p.AdminPassword) < 40 || len(p.ReaderPassword) < 40 || strings.ContainsRune(p.AdminPassword, 0) || strings.ContainsRune(p.ReaderPassword, 0) {
		return nativeCopyPasswords{}, recovery.ErrConflict
	}
	return p, nil
}

func (s *nativeSQLCopyState) copyGuard(client *gcp.Session, r recovery.NativeRestore, cfg *planning.ResourceConfig, c recovery.NativeWorkerControl) func(context.Context) error {
	if s.Version == 2 {
		return s.pitrCopyGuard(client, r, cfg, c)
	}
	return func(ctx context.Context) error {
		if err := c.Check(ctx); err != nil {
			return err
		}
		if s.PhysicalID == "" {
			return recovery.ErrConflict
		}
		in, err := client.GetSQLInstance(ctx, "lp-recovery-"+s.Scope[:32])
		if err != nil {
			return err
		}
		pin, err := in.Incarnation(r.Target.NativeProject, r.Target.Region, "lp-recovery-"+s.Scope[:32])
		if err != nil {
			return err
		}
		if pin != s.PhysicalID || in.Kind != "sql#instance" || in.DatabaseVersion != "POSTGRES_17" || in.Settings.UserLabels[gcp.SQLIntentLabel] != s.Scope[:60] || in.Settings.UserLabels["managed-by"] != "loomplane" {
			return recovery.ErrConflict
		}
		if settingsErr := in.CheckRecoverySettings(s.spec(r, cfg)); settingsErr != nil {
			return settingsErr
		}
		return c.Check(ctx)
	}
}

func (s *nativeSQLCopyState) spec(r recovery.NativeRestore, cfg *planning.ResourceConfig) gcp.SQLInstanceSpec {
	if s.Version == 2 {
		spec := cloudSQLPITRSourceSpec(cfg, "lp-recovery-"+s.Scope[:32])
		spec.Intent = s.Scope[:60]
		return spec
	}
	return gcp.SQLInstanceSpec{Name: "lp-recovery-" + s.Scope[:32], DatabaseVersion: "POSTGRES_17", Region: r.Target.Region, Tier: cfg.Managed.Profile.ComputeClass, Storage: &gcp.SQLStorageSettings{SizeGB: cfg.Managed.Profile.StorageGB, Type: cfg.Managed.Profile.StorageType}, Intent: s.Scope[:60]}
}

func (s *nativeSQLCopyState) restore(ctx context.Context, client *gcp.Session, r recovery.NativeRestore, cfg *planning.ResourceConfig, c recovery.NativeWorkerControl) error {
	if s.Version == 2 {
		return s.clonePITR(ctx, client, r, cfg, c)
	}
	if s.Uncertain && s.Operation == "" {
		return errNativeReconcile
	}
	guard := func(ctx context.Context) error {
		if err := c.Check(ctx); err != nil {
			return err
		}
		if s.PhysicalID != "" {
			return s.copyGuard(client, r, cfg, c)(ctx)
		}
		return nil
	}
	spec := s.spec(r, cfg)
	spec.Inspect = func(in *gcp.SQLInstance) error {
		pin, pinErr := in.Incarnation(r.Target.NativeProject, r.Target.Region, "lp-recovery-"+s.Scope[:32])
		if pinErr != nil {
			return pinErr
		}
		if s.PhysicalID != "" && s.PhysicalID != pin {
			return recovery.ErrConflict
		}
		s.PhysicalID = pin
		return s.save(ctx, c)
	}
	_, err := client.RestoreSQLBackup(ctx, r.Point.ProviderID, spec, s.track(ctx, c, guard, false))
	return err
}

// RestoreNative creates only an isolated copy, then proves an actual SQL login.
// The original binding and the source database credentials are never changed.
func (n *NativeDatabases) RestoreNative(ctx context.Context, p security.Principal, r recovery.NativeRestore, c recovery.NativeCopyControl) (recovery.NativeRestoreResult, error) {
	var result recovery.NativeRestoreResult
	var err error
	if r.Target.Provider == "rds" {
		result, err = n.restoreRDSCopy(ctx, p, r, c)
	} else {
		result, err = n.restoreCloudSQLCopy(ctx, p, r, c)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || aws.DBRecoveryReconciliationPending(err) {
		return recovery.NativeRestoreResult{}, errors.Join(recovery.ErrNativePending, err)
	}
	return result, err
}

func (n *NativeDatabases) restoreCloudSQLCopy(ctx context.Context, p security.Principal, r recovery.NativeRestore, c recovery.NativeCopyControl) (recovery.NativeRestoreResult, error) {
	if c.Check == nil || c.Save == nil || c.Selected == nil || p.OrgID != r.OrgID || p.UserID == "" {
		return recovery.NativeRestoreResult{}, recovery.ErrInvalid
	}
	if err := c.Check(ctx); err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	s, cfg, err := readNativeSQLCopy(r)
	if err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	if s.Deleted || s.DeleteUncertain || !r.ExpiresAt.After(time.Now()) {
		return recovery.NativeRestoreResult{}, recovery.ErrConflict
	}
	client, err := n.nativeCopySession(ctx, r)
	if err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	passwords, err := n.copyPasswords(ctx, r, s, c.NativeWorkerControl)
	if err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	if s.Stage == "restore" {
		if !s.Uncertain {
			selected, selectErr := c.Selected(ctx)
			if selectErr != nil {
				return recovery.NativeRestoreResult{}, selectErr
			}
			if selectionErr := checkNativeCopySelection(r, selected); selectionErr != nil {
				return recovery.NativeRestoreResult{}, selectionErr
			}
		}
		if checkErr := s.restore(ctx, client, r, cfg, c.NativeWorkerControl); checkErr != nil {
			return recovery.NativeRestoreResult{}, checkErr
		}
		s.Stage, s.Operation, s.Uncertain = "admin", "", false
		if s.Version == 2 {
			s.Stage = "network"
		}
		if checkErr := s.save(ctx, c.NativeWorkerControl); checkErr != nil {
			return recovery.NativeRestoreResult{}, checkErr
		}
	}
	if s.Stage == "network" {
		if checkErr := s.preparePITR(ctx, client, r, cfg, c.NativeWorkerControl); checkErr != nil {
			return recovery.NativeRestoreResult{}, checkErr
		}
		s.Stage, s.Operation, s.Uncertain = "admin", "", false
		if checkErr := s.save(ctx, c.NativeWorkerControl); checkErr != nil {
			return recovery.NativeRestoreResult{}, checkErr
		}
	}
	guard := s.copyGuard(client, r, cfg, c.NativeWorkerControl)
	if s.Stage == "admin" {
		if s.Uncertain && s.Operation == "" {
			return recovery.NativeRestoreResult{}, errNativeReconcile
		}
		if checkErr := client.SetUser(ctx, "lp-recovery-"+s.Scope[:32], "postgres", passwords.AdminPassword, s.track(ctx, c.NativeWorkerControl, guard, false)); checkErr != nil {
			return recovery.NativeRestoreResult{}, nativeCredentialMutationError{cause: checkErr}
		}
		s.Stage, s.Operation, s.Uncertain = "verify", "", false
		if checkErr := s.save(ctx, c.NativeWorkerControl); checkErr != nil {
			return recovery.NativeRestoreResult{}, checkErr
		}
	}
	if checkErr := guard(ctx); checkErr != nil {
		return recovery.NativeRestoreResult{}, checkErr
	}
	if checkErr := n.checkNativeSQL(ctx, client, r, s, passwords, guard, false); checkErr != nil {
		return recovery.NativeRestoreResult{}, checkErr
	}
	s.Stage = "ready"
	if checkErr := s.save(ctx, c.NativeWorkerControl); checkErr != nil {
		return recovery.NativeRestoreResult{}, checkErr
	}
	return recovery.NativeRestoreResult{ExternalID: r.Target.NativeProject + ":" + r.Target.Region + ":lp-recovery-" + s.Scope[:32], PhysicalID: s.PhysicalID, SQLVerified: true}, nil
}

func (n *NativeDatabases) checkNativeSQL(ctx context.Context, client *gcp.Session, r recovery.NativeRestore, s *nativeSQLCopyState, passwords nativeCopyPasswords, guard func(context.Context) error, existingOnly bool) error {
	var connector gcp.SQLConnector
	var err error
	switch {
	case n.SQLConnector != nil:
		connector, err = n.SQLConnector(ctx, client, r.Target.Region, "lp-recovery-"+s.Scope[:32])
	case s.Version == 2:
		connector, err = client.PostgresPSCConnector(ctx, r.Target.Region, "lp-recovery-"+s.Scope[:32])
	default:
		connector, err = client.PostgresConnector(ctx, r.Target.Region, "lp-recovery-"+s.Scope[:32])
	}
	if err != nil {
		return err
	}
	return verifyNativeReader(ctx, r, s, passwords, guard, connector, "postgres", existingOnly)
}

type nativeSQLConnector interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	Close() error
}

func verifyNativeReader(ctx context.Context, r recovery.NativeRestore, s *nativeSQLCopyState, passwords nativeCopyPasswords, guard func(context.Context) error, connector nativeSQLConnector, adminUser string, existingOnly bool) error {
	var err error
	if connector == nil {
		return recovery.ErrInvalid
	}
	defer func() { _ = connector.Close() }()
	verifier := &nativepg.Verifier{Dial: connector.DialContext}
	spec := nativepg.Spec{Database: databaseName(r.Source.Name), AdminUser: adminUser, AdminPassword: passwords.AdminPassword, Reader: "lp_recovery_" + s.Scope[:32], ReaderPassword: passwords.ReaderPassword, Intent: s.Scope, ExpiresAt: r.ExpiresAt, MajorVersion: 17}
	if existingOnly {
		_, err = verifier.Inspect(ctx, spec, guard)
	} else {
		_, err = verifier.Verify(ctx, spec, guard)
	}
	return err
}

// DeleteNative retains the pin and operation through retries. Secrets are
// removed only after the destination confirms the temporary copy is absent.
func (n *NativeDatabases) DeleteNative(ctx context.Context, p security.Principal, r recovery.NativeRestore, c recovery.NativeWorkerControl) (recovery.NativeCleanupResult, error) {
	if r.Target.Provider == "rds" {
		return n.deleteRDSCopy(ctx, p, r, c)
	}
	if c.Check == nil || c.Save == nil || p.OrgID != r.OrgID || p.UserID == "" {
		return recovery.NativeCleanupResult{}, recovery.ErrInvalid
	}
	if err := c.Check(ctx); err != nil {
		return recovery.NativeCleanupResult{}, err
	}
	s, cfg, err := readNativeSQLCopy(r)
	if err != nil {
		return recovery.NativeCleanupResult{}, err
	}
	client, err := n.nativeCopySession(ctx, r)
	if err != nil {
		return recovery.NativeCleanupResult{}, err
	}
	if !s.Deleted && s.PhysicalID == "" && s.Uncertain {
		if s.Operation == "" {
			return recovery.NativeCleanupResult{}, errNativeReconcile
		}
		spec := s.spec(r, cfg)
		spec.Inspect = func(in *gcp.SQLInstance) error {
			pin, pinErr := in.Incarnation(r.Target.NativeProject, r.Target.Region, spec.Name)
			if pinErr != nil {
				return pinErr
			}
			s.PhysicalID = pin
			return s.save(ctx, c)
		}
		inspect := client.InspectSQLRecoveryForCleanup
		operation := s.Operation
		if s.Version == 2 {
			inspect, operation = client.InspectSQLPITRForCleanup, s.CloneOperation
		}
		if _, observeErr := inspect(ctx, spec, operation, c.Check); observeErr != nil {
			return recovery.NativeCleanupResult{}, observeErr
		}
	}
	name := "lp-recovery-" + s.Scope[:32]
	if s.DeleteUncertain && s.DeleteOperation == "" {
		return recovery.NativeCleanupResult{}, errNativeReconcile
	}
	if s.PhysicalID != "" && !s.Deleted {
		remove, witness := client.DeleteSQLRecovery, s.Scope[:60]
		if s.Version == 2 {
			remove, witness = client.DeleteSQLPITRCopy, s.CloneOperation
		}
		if checkErr := remove(ctx, name, r.Target.Region, s.PhysicalID, witness, s.track(ctx, c, c.Check, true)); checkErr != nil {
			return recovery.NativeCleanupResult{}, checkErr
		}
	}
	if checkErr := c.Check(ctx); checkErr != nil {
		return recovery.NativeCleanupResult{}, checkErr
	}
	if _, err = client.GetSQLInstance(ctx, name); !gcp.IsNotFound(err) {
		if err == nil {
			err = recovery.ErrConflict
		}
		return recovery.NativeCleanupResult{}, err
	}
	s.Deleted, s.DeleteUncertain, s.DeleteOperation = true, false, ""
	if checkErr := s.save(ctx, c); checkErr != nil {
		return recovery.NativeCleanupResult{}, checkErr
	}
	secretCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if checkErr := n.Secrets.Delete(secretCtx, nativeCopySecretKey(r)); checkErr != nil {
		return recovery.NativeCleanupResult{}, checkErr
	}
	if checkErr := c.Check(ctx); checkErr != nil {
		return recovery.NativeCleanupResult{}, checkErr
	}
	return recovery.NativeCleanupResult{ConfirmedAbsent: true}, nil
}

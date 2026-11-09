package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/memdriver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func TestRestoreComponentFailureCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, fault string
		persist     bool
		want        []string
	}{
		{name: "runtime secret rejected", fault: "runtime-put", want: []string{"runtime-put", "files-delete", "runtime-delete"}},
		{name: "runtime secret persisted before error", fault: "runtime-put", persist: true, want: []string{"runtime-put", "files-delete", "runtime-delete"}},
		{name: "runtime files rejected", fault: "files-put", want: []string{"runtime-put", "files-put", "files-delete", "runtime-delete"}},
		{name: "runtime files persisted before error", fault: "files-put", persist: true, want: []string{"runtime-put", "files-put", "files-delete", "runtime-delete"}},
		{name: "create failed before row", fault: "create", want: []string{"runtime-put", "files-put", "create", "files-delete", "runtime-delete"}},
		{name: "create failed after row", fault: "create", persist: true, want: []string{"runtime-put", "files-put", "create", "apply", "purge", "delete-row", "remove", "files-delete", "runtime-delete"}},
		{name: "import partially wrote data", fault: "import", want: []string{"runtime-put", "files-put", "create", "apply", "import", "purge", "delete-row", "remove", "files-delete", "runtime-delete"}},
		{name: "configure partially wrote credentials", fault: "configure", want: []string{"runtime-put", "files-put", "create", "apply", "import", "configure", "purge", "delete-row", "remove", "files-delete", "runtime-delete"}},
		{name: "probe rejected clone", fault: "probe", want: []string{"runtime-put", "files-put", "create", "apply", "import", "configure", "probe", "purge", "delete-row", "remove", "files-delete", "runtime-delete"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFailureFixture(t)
			f.fault, f.persist = tc.fault, tc.persist
			clone, err := f.restore(f.ctx)
			if clone != "" || !errors.Is(err, f.failure) {
				t.Fatalf("failed restore returned clone %q, error %v", clone, err)
			}
			f.assertClean()
			f.assertEvents(tc.want)
		})
	}
}

func TestRestoreComponentRetainsFailedPurgeForExactRetry(t *testing.T) {
	f := newRecoveryFailureFixture(t)
	f.fault, f.cleanupFault = "probe", "purge"
	clone, err := f.restore(f.ctx)
	if clone != "" || !errors.Is(err, f.failure) || !errors.Is(err, f.cleanupFailure) {
		t.Fatalf("restore lost primary or cleanup error: clone %q, error %v", clone, err)
	}
	retained, err := f.instances.GetBySlug(f.ctx, f.cloneName)
	if err != nil {
		t.Fatalf("failed purge lost the clone row: %v", err)
	}
	if !bytes.Equal(f.native.data[retained.ID], f.payload) || f.native.passwords[retained.ID] == "" {
		t.Fatal("failed purge discarded the clone data or configured credentials")
	}
	keptValues := bytes.Clone(f.base.secrets.m[f.cloneRef])
	keptFiles := bytes.Clone(f.base.secrets.m[f.cloneRef+runtimeFilesSuffix])
	if len(keptValues) == 0 || len(keptFiles) == 0 {
		t.Fatal("failed purge removed the runtime secret or files")
	}
	f.assertSource()
	f.assertEvents([]string{"runtime-put", "files-put", "create", "apply", "import", "configure", "probe", "purge"})
	f.events = nil
	f.cleanupFault = ""
	if cleanupErr := f.base.target.DeleteRecovery(f.ctx, f.principal, f.binding, f.operation); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if !slices.Equal(f.native.purged, []id.ID{retained.ID, retained.ID}) || f.instances.deleted != retained.ID {
		t.Fatal("cleanup retry did not target the retained clone ID")
	}
	f.assertClean()
	f.assertEvents([]string{"purge", "delete-row", "remove", "files-delete", "runtime-delete"})
	f.events = nil
	if cleanupErr := f.base.target.DeleteRecovery(f.ctx, f.principal, f.binding, f.operation); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	f.assertEvents([]string{"files-delete", "runtime-delete"})
	f.assertClean()
}

func TestRestoreComponentRecordsSecretCleanupFailureAndRetries(t *testing.T) {
	for _, stage := range []string{"files-delete", "runtime-delete"} {
		t.Run(stage, func(t *testing.T) {
			f := newRecoveryFailureFixture(t)
			f.fault, f.cleanupFault = "probe", stage
			_, err := f.restore(f.ctx)
			if !errors.Is(err, f.failure) || !errors.Is(err, f.cleanupFailure) {
				t.Fatalf("restore lost a cleanup error: %v", err)
			}
			if _, lookupErr := f.instances.GetBySlug(f.ctx, f.cloneName); !errors.Is(lookupErr, ctrlplane.ErrNotFound) {
				t.Fatalf("purged clone row remains: %v", lookupErr)
			}
			remaining := f.cloneRef
			if stage == "files-delete" {
				remaining += runtimeFilesSuffix
			}
			if len(f.base.secrets.m[remaining]) == 0 {
				t.Fatal("failed secret deletion did not leave a retryable value")
			}
			f.assertSource()
			f.cleanupFault, f.events = "", nil
			if cleanupErr := f.base.target.DeleteRecovery(f.ctx, f.principal, f.binding, f.operation); cleanupErr != nil {
				t.Fatal(cleanupErr)
			}
			f.assertEvents([]string{"files-delete", "runtime-delete"})
			f.assertClean()
		})
	}
}

func TestRestoreComponentCleanupDetachesCanceledParentAndKeepsDeadline(t *testing.T) {
	f := newRecoveryFailureFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.cancel, f.fault = cancel, "import"
	_, err := f.restore(ctx)
	if !errors.Is(err, context.Canceled) || ctx.Err() != context.Canceled {
		t.Fatalf("restore did not return the canceled import: %v", err)
	}
	f.assertClean()
	f.assertEvents([]string{"runtime-put", "files-put", "create", "apply", "import", "purge", "delete-row", "remove", "files-delete", "runtime-delete"})
}

func TestRestoreComponentSuccessKeepsIndependentCloneUntilExplicitCleanup(t *testing.T) {
	f := newRecoveryFailureFixture(t)
	cloneID, err := f.restore(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := f.instances.GetBySlug(f.ctx, f.cloneName)
	if err != nil || clone.ID.String() != cloneID || clone.ID == f.source.ID {
		t.Fatalf("restore returned the wrong clone: %q, %v", cloneID, err)
	}
	if !bytes.Equal(f.native.data[clone.ID], f.payload) || f.native.passwords[clone.ID] == "" || f.native.passwords[clone.ID] == f.native.passwords[f.source.ID] {
		t.Fatal("successful restore lost data or reused source credentials")
	}
	f.assertEvents([]string{"runtime-put", "files-put", "create", "apply", "import", "configure", "probe"})
	f.assertSource()
	f.events = nil
	if cleanupErr := f.base.target.DeleteRecovery(f.ctx, f.principal, f.binding, f.operation); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	f.assertClean()
	f.assertEvents([]string{"purge", "delete-row", "remove", "files-delete", "runtime-delete"})
}

type recoveryFailureFixture struct {
	t                                         *testing.T
	base                                      *componentFixture
	ctx                                       context.Context
	principal                                 security.Principal
	binding                                   *planning.Binding
	config                                    *planning.ResourceConfig
	descriptor                                recovery.Descriptor
	files                                     *backupfiles.Store
	object                                    backupfiles.Object
	operation, cloneName, cloneRef, sourceRef string
	source                                    *instance.Instance
	sourceSnapshot                            []byte
	sourceSecrets                             map[string][]byte
	payload                                   []byte
	instances                                 *recoveryFaultInstances
	native                                    *recoveryFaultBackend
	events                                    []string
	fault, cleanupFault                       string
	persist                                   bool
	failure, cleanupFailure                   error
	cancel                                    context.CancelFunc
}

func newRecoveryFailureFixture(t *testing.T) *recoveryFailureFixture {
	t.Helper()
	registry := recipes.Default()
	base := newComponentFixture(t, registry)
	f := &recoveryFailureFixture{t: t, base: base, principal: security.Principal{OrgID: "org", UserID: "usr"}, payload: []byte("native exported application documents"), failure: errors.New("recovery operation failed"), cleanupFailure: errors.New("recovery cleanup failed")}
	f.ctx = infrastructure.WithPrincipal(t.Context(), f.principal)
	composition, err := registry.Resolve("opensearch", "", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	f.config = &planning.ResourceConfig{Kind: "opensearch", Version: composition.Version, Provider: "server", Target: "conn", TargetSettings: map[string]string{"architecture": "arm64"}, Component: &planning.ComponentConfig{Kind: "opensearch", Resource: "source", Revision: composition.Revision, Component: composition.Components[0], Protection: recipes.ProtectionNative}}
	f.descriptor, err = recovery.Describe(f.config, registry)
	if err != nil {
		t.Fatal(err)
	}
	f.binding = &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Name: "source", Provider: "server", ConnectionID: "conn", Subject: planning.ResourceSubject}
	f.binding.Config, err = json.Marshal(f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.sourceRef = resourceKey("org", "env", "source", "runtime_existing")
	base.secrets.m[f.sourceRef] = []byte(`{"LOOMPLANE_APP_PASSWORD":"source-password-never-rotate"}`)
	base.secrets.m[f.sourceRef+runtimeFilesSuffix] = []byte("source files and trust must remain unchanged")
	f.source, err = base.instances.Create(f.ctx, instance.CreateRequest{Name: "source", ProviderName: base.target.Register("org", "conn"), Services: []cpprovider.ServiceSpec{{Name: mainService, Image: f.descriptor.Image}}, Labels: map[string]string{owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: "env", owned.LabelConnection: "conn", owned.LabelService: "source", owned.LabelRuntime: f.sourceRef}})
	if err != nil {
		t.Fatal(err)
	}
	f.binding.ExternalID = f.source.ID.String()
	f.sourceSnapshot, err = json.Marshal(f.source)
	if err != nil {
		t.Fatal(err)
	}
	f.sourceSecrets = map[string][]byte{}
	for key, value := range base.secrets.m {
		f.sourceSecrets[key] = bytes.Clone(value)
	}
	operation, err := typeid.Generate("restore")
	if err != nil {
		t.Fatal(err)
	}
	f.operation = operation.String()
	f.cloneName, f.cloneRef, err = recoveryIdentity(f.binding, f.operation)
	if err != nil {
		t.Fatal(err)
	}
	f.instances = &recoveryFaultInstances{Service: base.instances, f: f}
	f.native = &recoveryFaultBackend{componentBackend: base.backend, f: f, data: map[id.ID][]byte{f.source.ID: []byte("source live data after backup")}, passwords: map[id.ID]string{f.source.ID: "source-password-never-rotate"}}
	base.target.Ctrl.Instances = f.instances
	base.target.Secrets = &recoveryFaultSecrets{memSecrets: base.secrets, f: f}
	// Keep the existing provider registration, but give it the recording adapter.
	provider, err := base.target.Ctrl.Providers.Get(base.target.Register("org", "conn"))
	if err != nil {
		t.Fatal(err)
	}
	provider.(*owned.Provider).Backend = f.native
	base.target.Backend = func(string, string) owned.Backend { return f.native }
	driver := memdriver.New()
	if openErr := driver.Open(f.ctx, "mem://"); openErr != nil {
		t.Fatal(openErr)
	}
	files, err := trove.Open(driver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := files.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if bucketErr := files.CreateBucket(f.ctx, "backups"); bucketErr != nil {
		t.Fatal(bucketErr)
	}
	f.files, err = backupfiles.New(files, "backups", "mem://")
	if err != nil {
		t.Fatal(err)
	}
	f.object, err = enginebackup.Export(f.ctx, f.files, "org/source-point", recoverySource(f.binding, f.descriptor), func(_ context.Context, out io.Writer) error {
		_, writeErr := out.Write(f.payload)
		return writeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *recoveryFailureFixture) restore(ctx context.Context) (string, error) {
	return f.base.target.restoreComponent(ctx, f.principal, f.binding, f.config, f.descriptor, f.files, f.object, f.operation)
}

func (f *recoveryFailureFixture) assertEvents(want []string) {
	f.t.Helper()
	if !slices.Equal(f.events, want) {
		f.t.Fatalf("recovery effects = %v, want %v", f.events, want)
	}
}

func (f *recoveryFailureFixture) assertSource() {
	f.t.Helper()
	source, err := f.instances.Get(f.ctx, f.source.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	raw, err := json.Marshal(source)
	if err != nil || !bytes.Equal(raw, f.sourceSnapshot) || !bytes.Equal(f.native.data[f.source.ID], []byte("source live data after backup")) || f.native.passwords[f.source.ID] != "source-password-never-rotate" {
		f.t.Fatalf("recovery changed the source instance, live data or credentials: %v", err)
	}
	for key, want := range f.sourceSecrets {
		if !bytes.Equal(f.base.secrets.m[key], want) {
			f.t.Fatalf("recovery changed source secret %q", key)
		}
	}
	if len(f.native.data) > 2 || len(f.native.passwords) > 2 {
		f.t.Fatal("recovery changed an unrelated runtime identity")
	}
}

func (f *recoveryFailureFixture) assertClean() {
	f.t.Helper()
	f.assertSource()
	if _, err := f.instances.GetBySlug(f.ctx, f.cloneName); !errors.Is(err, ctrlplane.ErrNotFound) {
		f.t.Fatalf("clone row remains after cleanup: %v", err)
	}
	if !reflect.DeepEqual(f.base.secrets.m, f.sourceSecrets) || len(f.native.data) != 1 || len(f.native.passwords) != 1 {
		f.t.Fatal("cleanup left clone files, credentials or native data")
	}
	f.base.backend.mu.Lock()
	defer f.base.backend.mu.Unlock()
	if len(f.base.backend.specs) != 1 {
		f.t.Fatal("cleanup left a clone container or removed the source container")
	}
	if _, exists := f.base.backend.specs[f.source.ID]; !exists {
		f.t.Fatal("cleanup removed the source container")
	}
}

func (f *recoveryFailureFixture) cleanupContext(ctx context.Context) {
	f.t.Helper()
	deadline, ok := ctx.Deadline()
	if ctx.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Minute {
		f.t.Fatal("cleanup needs an uncanceled context with a bounded deadline")
	}
}

func (f *recoveryFailureFixture) cloneValues() {
	f.t.Helper()
	var values map[string]string
	if err := json.Unmarshal(f.base.secrets.m[f.cloneRef], &values); err != nil || values["LOOMPLANE_APP_PASSWORD"] == "" || values["LOOMPLANE_APP_PASSWORD"] == "source-password-never-rotate" {
		f.t.Fatalf("clone runtime credentials were not persisted independently: %v", err)
	}
	var files []runtimeFile
	if err := json.Unmarshal(f.base.secrets.m[f.cloneRef+runtimeFilesSuffix], &files); err != nil || len(files) == 0 {
		f.t.Fatalf("clone runtime files were not persisted before creation: %v", err)
	}
}

type recoveryFaultSecrets struct {
	*memSecrets
	f *recoveryFailureFixture
}

func (s *recoveryFaultSecrets) stage(key, action string) string {
	s.f.t.Helper()
	switch key {
	case s.f.cloneRef:
		return "runtime-" + action
	case s.f.cloneRef + runtimeFilesSuffix:
		return "files-" + action
	default:
		s.f.t.Fatalf("recovery mutated secret outside the deterministic clone: %q", key)
		return ""
	}
}

func (s *recoveryFaultSecrets) Put(ctx context.Context, key string, value []byte) error {
	stage := s.stage(key, "put")
	s.f.events = append(s.f.events, stage)
	if stage == s.f.fault && !s.f.persist {
		return s.f.failure
	}
	if err := s.memSecrets.Put(ctx, key, bytes.Clone(value)); err != nil {
		return err
	}
	if stage == s.f.fault {
		return s.f.failure
	}
	return nil
}

func (s *recoveryFaultSecrets) Delete(ctx context.Context, key string) error {
	stage := s.stage(key, "delete")
	s.f.events = append(s.f.events, stage)
	if s.f.cancel != nil {
		s.f.cleanupContext(ctx)
	}
	if stage == s.f.cleanupFault {
		return s.f.cleanupFailure
	}
	return s.memSecrets.Delete(ctx, key)
}

type recoveryFaultInstances struct {
	instance.Service
	f       *recoveryFailureFixture
	deleted id.ID
}

func (s *recoveryFaultInstances) Create(ctx context.Context, request instance.CreateRequest) (*instance.Instance, error) {
	f := s.f
	f.events = append(f.events, "create")
	if request.Name != f.cloneName || request.Labels[owned.LabelRuntime] != f.cloneRef || request.Labels[server.LabelRecovery] != f.operation || request.Labels[owned.LabelPublic] != "false" {
		f.t.Fatal("create targeted an identity outside the deterministic clone")
	}
	f.cloneValues()
	if f.fault == "create" && !f.persist {
		return nil, f.failure
	}
	created, err := s.Service.Create(ctx, request)
	if err != nil {
		return nil, err
	}
	if f.fault == "create" {
		return nil, f.failure
	}
	return created, nil
}

func (s *recoveryFaultInstances) Delete(ctx context.Context, iid id.ID) error {
	s.f.events = append(s.f.events, "delete-row")
	if s.f.cancel != nil {
		s.f.cleanupContext(ctx)
	}
	s.f.cloneValues()
	retained, err := s.GetBySlug(ctx, s.f.cloneName)
	if err != nil || iid != retained.ID || iid == s.f.source.ID {
		s.f.t.Fatalf("delete targeted the wrong row: %v", err)
	}
	if _, exists := s.f.native.data[iid]; exists {
		s.f.t.Fatal("clone row deletion preceded native data purge")
	}
	s.deleted = iid
	return s.Service.Delete(ctx, iid)
}

type recoveryFaultBackend struct {
	*componentBackend
	f         *recoveryFailureFixture
	data      map[id.ID][]byte
	passwords map[id.ID]string
	purged    []id.ID
}

func (b *recoveryFaultBackend) record(ctx context.Context, stage string, iid id.ID, labels map[string]string) {
	b.f.t.Helper()
	if iid == b.f.source.ID || labels[owned.LabelService] != b.f.cloneName || labels[owned.LabelRuntime] != b.f.cloneRef || labels[server.LabelRecovery] != b.f.operation {
		b.f.t.Fatalf("%s targeted a runtime outside the deterministic clone", stage)
	}
	var source enginebackup.Identity
	if err := json.Unmarshal([]byte(labels[enginebackup.LabelSource]), &source); err != nil || source != recoverySource(b.f.binding, b.f.descriptor) {
		b.f.t.Fatalf("%s lost the captured source identity: %v", stage, err)
	}
	b.f.cloneValues()
	b.f.events = append(b.f.events, stage)
	if b.f.cancel != nil && (stage == "purge" || stage == "remove") {
		b.f.cleanupContext(ctx)
	}
}

func (b *recoveryFaultBackend) Apply(ctx context.Context, iid id.ID, spec cpprovider.ServiceSpec, labels map[string]string) (*cpprovider.ProvisionResult, error) {
	b.record(ctx, "apply", iid, labels)
	return b.componentBackend.Apply(ctx, iid, spec, labels)
}

func (b *recoveryFaultBackend) RestoreDatabase(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	b.record(ctx, "import", iid, labels)
	if files != b.f.files || object != b.f.object {
		b.f.t.Fatal("import received another recovery point")
	}
	verified, err := enginebackup.Verified(ctx, files, object, recoverySource(b.f.binding, b.f.descriptor))
	if err != nil {
		return err
	}
	payload, readErr := io.ReadAll(verified)
	if payloadErr := errors.Join(readErr, closeVerified(verified)); payloadErr != nil {
		return payloadErr
	}
	if !bytes.Equal(payload, b.f.payload) {
		b.f.t.Fatal("import payload differs from the exported recovery point")
	}
	b.data[iid] = bytes.Clone(payload)
	if b.f.cancel != nil {
		b.f.cancel()
		return ctx.Err()
	}
	if b.f.fault == "import" {
		return b.f.failure
	}
	return nil
}

func (b *recoveryFaultBackend) ConfigureDatabase(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	b.record(ctx, "configure", iid, labels)
	if !bytes.Equal(b.data[iid], b.f.payload) || password == "" || password == b.passwords[b.f.source.ID] {
		b.f.t.Fatal("configuration preceded import or reused source credentials")
	}
	var values map[string]string
	if err := json.Unmarshal(b.f.base.secrets.m[b.f.cloneRef], &values); err != nil || values["LOOMPLANE_APP_PASSWORD"] != password {
		b.f.t.Fatalf("configured password differs from the persisted clone runtime: %v", err)
	}
	b.passwords[iid] = password
	if b.f.fault == "configure" {
		return b.f.failure
	}
	return nil
}

func (b *recoveryFaultBackend) DatabaseProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	b.record(ctx, "probe", iid, labels)
	if b.passwords[iid] == "" || !bytes.Equal(b.data[iid], b.f.payload) {
		b.f.t.Fatal("probe ran before import and credential configuration")
	}
	if b.f.fault == "probe" {
		return b.f.failure
	}
	return nil
}

func (b *recoveryFaultBackend) PurgeRecovery(ctx context.Context, iid id.ID, labels map[string]string) error {
	b.record(ctx, "purge", iid, labels)
	retained, err := b.f.instances.GetBySlug(ctx, b.f.cloneName)
	if err != nil || retained.ID != iid {
		b.f.t.Fatalf("purge lost its discoverable clone row: %v", err)
	}
	b.purged = append(b.purged, iid)
	if b.f.cleanupFault == "purge" {
		return b.f.cleanupFailure
	}
	delete(b.data, iid)
	delete(b.passwords, iid)
	return nil
}

func (b *recoveryFaultBackend) Remove(ctx context.Context, iid id.ID, labels map[string]string) error {
	b.record(ctx, "remove", iid, maps.Clone(labels))
	if _, exists := b.data[iid]; exists {
		b.f.t.Fatal("container removal preceded native data purge")
	}
	return b.componentBackend.Remove(ctx, iid, labels)
}

func (b *recoveryFaultBackend) BackupDatabase(context.Context, id.ID, map[string]string, *backupfiles.Store, string) (backupfiles.Object, error) {
	b.f.t.Fatal("clone recovery tried to take another source backup")
	return backupfiles.Object{}, b.f.failure
}

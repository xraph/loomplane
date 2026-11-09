//go:build qualification

package deployments

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
)

type duckReadonlyQualificationBackend struct {
	owned.DatabaseBackend
	mu                          sync.Mutex
	attempted                   []id.ID
	beforeRestore, afterRestore func(context.Context, id.ID, map[string]string) error
}

func (b *duckReadonlyQualificationBackend) Apply(ctx context.Context, iid id.ID, spec cpprovider.ServiceSpec, labels map[string]string) (*cpprovider.ProvisionResult, error) {
	b.mu.Lock()
	seen := false
	for _, previous := range b.attempted {
		seen = seen || previous.String() == iid.String()
	}
	if !seen {
		b.attempted = append(b.attempted, iid)
	}
	b.mu.Unlock()
	return b.DatabaseBackend.Apply(ctx, iid, spec, labels)
}

func (b *duckReadonlyQualificationBackend) allowFallback(ctx context.Context, c *server.Client) error {
	b.mu.Lock()
	attempted := append([]id.ID(nil), b.attempted...)
	b.mu.Unlock()
	for _, iid := range attempted {
		held, err := duckQualificationHeld(ctx, c, iid)
		if err != nil || held {
			return errors.Join(fmt.Errorf("native fixture retained for drained host reconciliation: instance=%s hold=%t", iid, held), err)
		}
	}
	return nil
}

func (b *duckReadonlyQualificationBackend) RestoreDatabase(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	if b.beforeRestore != nil {
		if err := b.beforeRestore(ctx, iid, labels); err != nil {
			return err
		}
	}
	if err := b.DatabaseBackend.RestoreDatabase(ctx, iid, labels, files, object); err != nil {
		return err
	}
	if b.afterRestore != nil {
		return b.afterRestore(ctx, iid, labels)
	}
	return nil
}

type duckQualificationApplyStub struct {
	owned.DatabaseBackend
	apply    func(id.ID) (*cpprovider.ProvisionResult, error)
	restores int
}

func (b *duckQualificationApplyStub) Apply(_ context.Context, iid id.ID, _ cpprovider.ServiceSpec, _ map[string]string) (*cpprovider.ProvisionResult, error) {
	return b.apply(iid)
}

func (b *duckQualificationApplyStub) RestoreDatabase(context.Context, id.ID, map[string]string, *backupfiles.Store, backupfiles.Object) error {
	b.restores++
	return nil
}

type duckQualificationTransport func(*http.Request) (*http.Response, error)

func (f duckQualificationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDuckQualificationFailedInitializationRetainsAllAttemptedApplyEvidence(t *testing.T) {
	for _, mode := range []string{"helper-create", "helper-start", "helper-wait"} {
		t.Run(mode, func(t *testing.T) {
			source, clone := id.New(id.PrefixInstance), id.New(id.PrefixInstance)
			resources := map[string]bool{}
			var events []string
			delegate := &duckQualificationApplyStub{}
			watch := &duckReadonlyQualificationBackend{DatabaseBackend: delegate}
			delegate.apply = func(iid id.ID) (*cpprovider.ProvisionResult, error) {
				watch.mu.Lock()
				seen := false
				for _, attempted := range watch.attempted {
					seen = seen || attempted.String() == iid.String()
				}
				watch.mu.Unlock()
				if !seen {
					t.Fatal("runtime effect began before Apply attempt was recorded")
				}
				events = append(events, "apply-recorded-before-effects")
				if iid.String() == source.String() {
					return &cpprovider.ProvisionResult{}, nil
				}
				for _, suffix := range []string{"", "-readonly-stage", "-mutation"} {
					resources[server.ContainerName(clone)+suffix] = true
				}
				events = append(events, "unknown-"+mode)
				return nil, errors.New("synthetic initialization response lost")
			}
			if _, err := watch.Apply(t.Context(), source, cpprovider.ServiceSpec{}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := watch.Apply(t.Context(), clone, cpprovider.ServiceSpec{}, nil); err == nil {
				_ = watch.RestoreDatabase(t.Context(), clone, nil, nil, backupfiles.Object{})
				t.Fatal("failed Create/Apply was accepted")
			}
			mutations := 0
			client := &server.Client{Config: &server.Config{}, HTTP: &http.Client{Transport: duckQualificationTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet && req.Method != http.MethodHead {
					mutations++
					delete(resources, strings.TrimPrefix(req.URL.Path, "/containers/"))
				}
				code, body := http.StatusNotFound, ""
				if req.URL.Path == "/containers/json" {
					code, body = http.StatusOK, "[]"
				}
				if req.URL.Path == "/containers/"+server.ContainerName(clone)+"-mutation/json" {
					code = http.StatusOK
					events = append(events, "failed-apply-hold-inspected")
				}
				return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			if err := watch.allowFallback(t.Context(), client); err == nil {
				for resource := range resources {
					response, deleteErr := client.Request(t.Context(), http.MethodDelete, "/containers/"+resource, nil)
					if deleteErr == nil {
						response.Body.Close()
					}
				}
				t.Fatal("fallback cleanup ignored failed initialization hold")
			}
			if mutations != 0 || delegate.restores != 0 || len(resources) != 3 || len(events) != 4 || events[3] != "failed-apply-hold-inspected" {
				t.Fatalf("failed initialization lost evidence: mutations=%d restores=%d resources=%d events=%v", mutations, delegate.restores, len(resources), events)
			}
		})
	}
}

func duckQualificationJSON(ctx context.Context, c *server.Client, path string, out any) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := c.Request(bounded, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("qualification inspection returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(out)
}

// A failed native run retains unresolved daemon effects for drained reconciliation.
func duckQualificationHeld(ctx context.Context, c *server.Client, iid id.ID) (bool, error) {
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	response, err := c.Request(bounded, http.MethodGet, "/containers/"+server.ContainerName(iid)+"-mutation/json", nil)
	if err != nil {
		return true, err
	}
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		return true, nil
	}
	if response.StatusCode != http.StatusNotFound {
		return true, errors.New("reservation absence could not be established")
	}
	var all []struct{ Labels map[string]string }
	if err := duckQualificationJSON(ctx, c, "/containers/json?all=true", &all); err != nil {
		return true, err
	}
	for _, item := range all {
		if item.Labels["loomplane.duckdb-readonly-stage"] == iid.String() {
			return true, nil
		}
	}
	return false, nil
}

type duckQualificationContainer struct {
	ID     string `json:"Id"`
	Mounts []struct {
		Type, Name, Destination string
		RW                      *bool
	}
	Config          struct{ Labels map[string]string }
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

func duckQualificationServing(ctx context.Context, c *server.Client, iid id.ID, labels map[string]string) (duckQualificationContainer, error) {
	var serving duckQualificationContainer
	if err := duckQualificationJSON(ctx, c, "/containers/"+server.ContainerName(iid)+"/json", &serving); err != nil {
		return serving, err
	}
	if serving.ID == "" || serving.Config.Labels[owned.LabelInstance] != iid.String() || serving.Config.Labels[server.LabelRecovery] != labels[server.LabelRecovery] {
		return serving, errors.New("qualification serving identity changed")
	}
	data := false
	for _, mount := range serving.Mounts {
		if strings.HasPrefix(mount.Destination, "/data/") {
			return serving, errors.New("nested recovery data mount")
		}
		if mount.Destination == "/data" || mount.Name == server.VolumeName(iid, "data") {
			if data || mount.Type != "volume" || mount.Name != server.VolumeName(iid, "data") || mount.Destination != "/data" || mount.RW == nil || *mount.RW {
				return serving, errors.New("recovery data mount is not exactly read-only")
			}
			data = true
		}
	}
	if !data {
		return serving, errors.New("recovery data mount missing")
	}
	var all []struct {
		ID     string `json:"Id"`
		Labels map[string]string
		Mounts *[]struct{ Type, Name string }
	}
	if err := duckQualificationJSON(ctx, c, "/containers/json?all=true", &all); err != nil {
		return serving, err
	}
	found := false
	for _, item := range all {
		if item.Mounts == nil {
			return serving, errors.New("attachment inventory incomplete")
		}
		if item.Labels["loomplane.duckdb-readonly-stage"] == iid.String() {
			return serving, errors.New("staging helper survived serving transition")
		}
		for _, mount := range *item.Mounts {
			if mount.Type == "volume" && mount.Name == server.VolumeName(iid, "data") {
				if found || item.ID != serving.ID {
					return serving, errors.New("unexpected recovery volume attachment")
				}
				found = true
			}
		}
	}
	if !found {
		return serving, errors.New("serving attachment absent from census")
	}
	return serving, nil
}

func duckQualificationCheckpointHash(ctx context.Context, c *server.Client, containerID string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := c.Request(bounded, http.MethodGet, "/containers/"+containerID+"/archive?path=/data/analytics.db", nil)
	if err != nil {
		return sum, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return sum, errors.New("checkpoint readback failed")
	}
	tr := tar.NewReader(response.Body)
	header, err := tr.Next()
	if err != nil || header.Name != "analytics.db" || header.Typeflag != tar.TypeReg || header.Size < 12 || header.Size > enginebackup.MaxPayload {
		return sum, errors.New("invalid checkpoint readback archive")
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, tr, header.Size); err != nil {
		return sum, err
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		return sum, errors.New("unexpected checkpoint readback entry")
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func duckQualificationCLI(ctx context.Context, socket, container, input string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	command := make([]string, 0, 10+len(args))
	command = append(command, "--host", "unix://"+socket, "exec", "-i", container, "/duckdb", "-batch", "-bail", "-csv", "-noheader")
	command = append(command, args...)
	child := exec.CommandContext(bounded, "docker", command...)
	child.Stdin = strings.NewReader(input)
	return child.CombinedOutput()
}

func duckQualificationRemote(ctx context.Context, socket, container, token, endpoint, ca, sql string) ([]byte, error) {
	input := "SET extension_directories=['/opt/duckdb/extensions']; LOAD httpfs; LOAD quack; SET enable_server_cert_verification=true; SET ca_cert_file='" + ca + "'; SELECT * FROM quack_query('" + endpoint + "', '" + strings.ReplaceAll(sql, "'", "''") + "', token := '" + token + "', disable_ssl := false);\n.quit\n"
	return duckQualificationCLI(ctx, socket, container, input)
}

func qualifyDuckReadonlyBoundary(ctx context.Context, t *testing.T, c *server.Client, socket string, iid id.ID, labels map[string]string, token string, wrongCA []byte) {
	t.Helper()
	serving, err := duckQualificationServing(ctx, c, iid, labels)
	if err != nil {
		t.Fatal(err)
	}
	before, err := duckQualificationCheckpointHash(ctx, c, serving.ID)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(iid.String(), "_", "-")
	marker, attach, copyPath, caPath := "/tmp/loomplane-boundary-"+suffix+".txt", "/tmp/loomplane-boundary-"+suffix+".duckdb", "/tmp/loomplane-boundary-"+suffix+".csv", "/tmp/loomplane-wrong-ca-"+suffix+".pem"
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, file := range []struct {
		path string
		data []byte
	}{{marker, []byte("loomplane-fixed-nonsecret-marker\n")}, {caPath, wrongCA}} {
		name := strings.TrimPrefix(file.path, "/tmp/")
		if headerErr := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(file.data))}); headerErr != nil {
			t.Fatal(headerErr)
		}
		if _, writeErr := tw.Write(file.data); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if closeErr := tw.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	response, err := c.RequestHeaders(bounded, http.MethodPut, "/containers/"+serving.ID+"/archive?path=/tmp", bytes.NewReader(raw.Bytes()), http.Header{"Content-Type": []string{"application/x-tar"}})
	if err != nil {
		cancel()
		t.Fatal("could not create harmless boundary fixtures")
	}
	response.Body.Close()
	cancel()
	if response.StatusCode != http.StatusOK {
		t.Fatal("boundary fixture upload failed")
	}
	if _, initErr := duckQualificationCLI(ctx, socket, serving.ID, "CHECKPOINT;\n.quit\n", attach); initErr != nil {
		t.Fatal("harmless separate ATTACH fixture could not be initialized")
	}
	remote := func(sql string) ([]byte, error) {
		return duckQualificationRemote(ctx, socket, serving.ID, token, "quack:localhost:9494", "/etc/loomplane/ca.pem", sql)
	}
	baseline, err := remote("SELECT 42")
	if err != nil || strings.TrimSpace(string(baseline)) != "42" {
		t.Fatal("verified TLS/auth baseline failed")
	}
	mode, err := remote("SELECT database_name, readonly FROM duckdb_databases() WHERE path='/data/analytics.db'")
	if err != nil || strings.TrimSpace(string(mode)) != "analytics,true" {
		t.Fatal("actual recovery database is not read-only")
	}
	deny := func(sql string) {
		t.Helper()
		out, deniedErr := remote(sql)
		if deniedErr == nil || (!bytes.Contains(bytes.ToLower(out), []byte("permission")) && !bytes.Contains(bytes.ToLower(out), []byte("configuration"))) {
			t.Fatal("fixed remote operation did not establish permission/configuration denial")
		}
	}
	deny("COPY (SELECT 42::INTEGER AS loomplane_readonly_probe) TO '" + copyPath + "' (FORMAT CSV, HEADER false)")
	deny("SELECT content FROM read_text('" + marker + "')")
	deny("ATTACH '" + attach + "' AS loomplane_boundary_fixture (READ_ONLY)")
	stateSQL := "SELECT current_setting('enable_external_access'), current_setting('lock_configuration'), current_setting('allowed_paths'), current_setting('allowed_directories'), current_setting('temp_directory')"
	state, err := remote(stateSQL)
	if err != nil || !strings.HasPrefix(string(state), "false,true,") {
		t.Fatal("hardened settings not established")
	}
	for _, sql := range []string{"SET enable_external_access=true", "RESET enable_external_access", "SET lock_configuration=false", "RESET lock_configuration", "SET allowed_paths=['" + copyPath + "']", "RESET allowed_paths", "SET allowed_directories=['/tmp']", "RESET allowed_directories"} {
		deny(sql)
		after, settingsErr := remote(stateSQL)
		if settingsErr != nil || !bytes.Equal(state, after) {
			t.Fatal("hardened settings changed in a later native session")
		}
	}
	newDataMarker := "/data/loomplane-boundary-new-" + suffix + ".csv"
	writeOut, writeErr := duckQualificationCLI(ctx, socket, serving.ID, "COPY (SELECT 42) TO '"+newDataMarker+"' (FORMAT CSV, HEADER false);\n.quit\n")
	if writeErr == nil || !bytes.Contains(bytes.ToLower(writeOut), []byte("read-only file system")) {
		t.Fatal("OS read-only mount did not deny a fixed new marker write-open")
	}
	for _, path := range []string{copyPath, newDataMarker} {
		bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
		markerResponse, markerErr := c.Request(bounded, http.MethodHead, "/containers/"+serving.ID+"/archive?path="+url.QueryEscape(path), nil)
		if markerErr != nil {
			cancel()
			t.Fatal("new marker absence could not be verified")
		}
		markerResponse.Body.Close()
		cancel()
		if markerResponse.StatusCode != http.StatusNotFound {
			t.Fatal("denied operation created its new marker")
		}
	}
	wrongCAOut, wrongCAErr := duckQualificationRemote(ctx, socket, serving.ID, token, "quack:localhost:9494", caPath, "SELECT 42")
	if wrongCAErr == nil || !bytes.Contains(bytes.ToLower(wrongCAOut), []byte("certificate")) {
		t.Fatal("wrong CA did not establish certificate rejection")
	}
	checkedIP := false
	for _, network := range serving.NetworkSettings.Networks {
		if network.IPAddress == "" {
			continue
		}
		checkedIP = true
		wrongHostOut, wrongHostErr := duckQualificationRemote(ctx, socket, serving.ID, token, "quack:"+network.IPAddress+":9494", "/etc/loomplane/ca.pem", "SELECT 42")
		if wrongHostErr == nil || !bytes.Contains(bytes.ToLower(wrongHostOut), []byte("certificate")) {
			t.Fatal("hostname mismatch did not establish certificate rejection")
		}
		break
	}
	if !checkedIP {
		t.Fatal("no private fixture address for certificate hostname check")
	}
	baseline, err = remote("SELECT 42")
	if err != nil || strings.TrimSpace(string(baseline)) != "42" {
		t.Fatal("verified TLS/auth did not remain available after denial checks")
	}
	after, err := duckQualificationCheckpointHash(ctx, c, serving.ID)
	if err != nil || after != before {
		t.Fatal("read-only boundary operations changed checkpoint bytes")
	}
	if _, err := duckQualificationServing(ctx, c, iid, labels); err != nil {
		t.Fatal(err)
	}
	t.Log("native read-only mount, marker EROFS/hash preservation, remote filesystem/settings denial and TLS rejection checks passed; no database overwrite attempted")
}

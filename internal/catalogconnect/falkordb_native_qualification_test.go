//go:build qualification

package catalogconnect

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

type falkorNativeFixture struct {
	Plain    string `json:"plain_access"`
	Verified string `json:"tls_access"`
	Denied   string `json:"denied_access"`
	Recovery string `json:"recovery_app_access"`
	Default  string `json:"default_access"`
}

func loadFalkorNativeFixture(t *testing.T) falkorNativeFixture {
	t.Helper()
	path := os.Getenv("LOOMPLANE_FALKORDB_FIXTURE_FILE")
	if !filepath.IsAbs(path) {
		t.Fatal("explicit private FalkorDB fixture file required")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("could not open the private FalkorDB fixture file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 512*1024 {
		t.Fatal("FalkorDB fixture file must be a bounded private regular file")
	}
	d := json.NewDecoder(io.LimitReader(f, 512*1024+1))
	d.DisallowUnknownFields()
	var fixture falkorNativeFixture
	if d.Decode(&fixture) != nil || d.Decode(new(any)) != io.EOF {
		t.Fatal("invalid private FalkorDB fixture file")
	}
	return fixture
}

func falkorNativePorts(t *testing.T, ctx context.Context) map[string]string {
	t.Helper()
	socket, container, arch := os.Getenv("LOOMPLANE_FALKORDB_DOCKER_SOCKET"), os.Getenv("LOOMPLANE_FALKORDB_CONTAINER"), os.Getenv("LOOMPLANE_FALKORDB_ARCH")
	if !filepath.IsAbs(socket) || container == "" || strings.HasPrefix(container, "-") || arch != "amd64" && arch != "arm64" {
		t.Fatal("explicit FalkorDB fixture socket, container and architecture required")
	}
	kind, ok := recipes.Default().Kind("falkordb")
	if !ok || len(kind.Releases) != 1 || kind.Releases[0].Product != "4.2.2" {
		t.Fatal("pinned FalkorDB 4.2.2 recipe required")
	}
	image := kind.Releases[0].Images["falkordb"]
	digest := image.Digests[arch]
	if digest == "" {
		t.Fatal("architecture pin missing")
	}
	inspect := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...)
		raw, err := cmd.Output()
		if err != nil || len(raw) > 16*1024 {
			t.Fatal("could not verify FalkorDB fixture metadata")
		}
		return raw
	}
	var actual struct {
		Image   string
		Running bool
		Fixture string
		Ports   map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
	}
	var pinned struct{ ID, Architecture string }
	const containerFields = `{"Image":{{json .Image}},"Running":{{json .State.Running}},"Fixture":{{json (index .Config.Labels "loomplane.falkordb.external-qualification")}},"Ports":{{json .NetworkSettings.Ports}}}`
	const imageFields = `{"ID":{{json .Id}},"Architecture":{{json .Architecture}}}`
	if json.Unmarshal(inspect("container", "inspect", "--format", containerFields, container), &actual) != nil ||
		json.Unmarshal(inspect("image", "inspect", "--format", imageFields, image.Repository+"@"+digest), &pinned) != nil ||
		!actual.Running || actual.Fixture != "1" || pinned.ID == "" || pinned.ID != actual.Image || pinned.Architecture != arch {
		t.Fatal("container did not match the disposable pinned FalkorDB fixture")
	}
	ports := make(map[string]string, 2)
	for mode, internalPort := range map[string]string{TLSDisable: "6379/tcp", TLSVerifyFull: "6380/tcp"} {
		bindings := actual.Ports[internalPort]
		if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort == "" {
			t.Fatal("FalkorDB fixture requires direct loopback ports for plaintext and TLS")
		}
		ports[mode] = bindings[0].HostPort
	}
	return ports
}

type falkorNativeCommandConn struct {
	net.Conn
	pending []string
	offset  int
}

func (c *falkorNativeCommandConn) Write(p []byte) (int, error) {
	if len(c.pending) == 0 || !strings.HasPrefix(c.pending[0][c.offset:], string(p)) {
		return 0, errors.New("unexpected FalkorDB fixture command")
	}
	n, err := c.Conn.Write(p)
	c.offset += n
	if c.offset == len(c.pending[0]) {
		c.pending = c.pending[1:]
		c.offset = 0
	}
	return n, err
}

const (
	falkorNativeGraph = "loomplane_fixture"
	// FalkorDB 4.2.2 names the query telemetry stream telemetry{<graph>}.
	falkorNativeTelemetry = "telemetry{" + falkorNativeGraph + "}"
)

type falkorNativeState struct {
	limit         int64
	keys          int64
	graphDump     string
	telemetryDump string
}

// Read fixture state on separate admin connections so the probe transcript
// contains only the two commands we want to qualify.
func snapshotFalkorNativeState(t *testing.T, ctx context.Context, raw, port string, expectedGraphs int) falkorNativeState {
	t.Helper()
	a, err := ParseEngineAccess(raw)
	if err != nil || a.Engine != EngineFalkorDB || a.Host != "127.0.0.1" || a.TLS != TLSDisable || strconv.Itoa(a.Port) != port || a.Username != "default" {
		t.Fatal("fixture state requires the disposable container's default user")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(a.Host, port))
	if err != nil {
		t.Fatal("could not open fixture state connection")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		t.Fatal("could not set fixture state deadline")
	}
	r := newFalkorRESP(conn)
	read := func(args ...string) falkorValue {
		t.Helper()
		if writeFalkorCommand(conn, args...) != nil {
			t.Fatal("fixture state request failed")
		}
		value, err := r.reply()
		if err != nil {
			t.Fatal("fixture state response failed")
		}
		return value
	}
	if validateFalkorHello(read("HELLO", "2", "AUTH", a.Username, a.Password)) != nil {
		t.Fatal("fixture state connection did not identify as FalkorDB")
	}
	config, keys, graphs := read("GRAPH.CONFIG", "GET", "RESULTSET_SIZE"), read("DBSIZE"), read("GRAPH.LIST")
	if !validFalkorConfig(config) {
		t.Fatal("fixture GRAPH.CONFIG GET RESULTSET_SIZE reply has invalid shape or value")
	}
	if keys.kind != ':' {
		t.Fatal("fixture DBSIZE reply is not an integer")
	}
	if graphs.kind != '*' {
		t.Fatal("fixture GRAPH.LIST reply is not an array")
	}
	if len(graphs.members) != expectedGraphs {
		t.Fatalf("fixture GRAPH.LIST count differs (got=%d, want=%d)", len(graphs.members), expectedGraphs)
	}
	wantKeys := int64(0)
	if expectedGraphs == 1 {
		graph := graphs.members[0]
		if graph.kind != '$' || graph.text != falkorNativeGraph {
			t.Fatal("fixture GRAPH.LIST did not return the seeded graph name")
		}
		wantKeys = 2
	}
	if keys.number != wantKeys {
		t.Fatalf("fixture DBSIZE differs (got=%d, want=%d)", keys.number, wantKeys)
	}
	out := falkorNativeState{limit: config.members[1].number, keys: keys.number}
	if expectedGraphs == 0 {
		telemetry := read("EXISTS", falkorNativeTelemetry)
		if telemetry.kind != ':' || telemetry.number != 0 {
			t.Fatal("empty fixture contains the telemetry key")
		}
		return out
	}
	// XLEN returns an integer, fails for other key types, and returns zero for a missing stream.
	entries := read("XLEN", falkorNativeTelemetry)
	if entries.kind != ':' || entries.number <= 0 {
		t.Fatal("fixture telemetry stream is absent or empty")
	}
	graph, telemetry := read("DUMP", falkorNativeGraph), read("DUMP", falkorNativeTelemetry)
	if graph.kind != '$' || graph.text == "" {
		t.Fatal("fixture graph snapshot failed")
	}
	if telemetry.kind != '$' || telemetry.text == "" {
		t.Fatal("fixture telemetry snapshot failed")
	}
	out.graphDump, out.telemetryDump = graph.text, telemetry.text
	return out
}

// TestQualifyFalkorDBExternalNative uses a labelled, disposable fixture.
// Prepare users and TLS outside the test using fixture-only credentials.
func TestQualifyFalkorDBExternalNative(t *testing.T) {
	if os.Getenv("LOOMPLANE_FALKORDB_NATIVE") != "1" {
		t.Skip("explicit FalkorDB external qualification required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	ports := falkorNativePorts(t, ctx)
	fixture := loadFalkorNativeFixture(t)
	expectedLimit, err := strconv.ParseInt(os.Getenv("LOOMPLANE_FALKORDB_RESULTSET_SIZE"), 10, 64)
	expectedGraphs, graphErr := strconv.Atoi(os.Getenv("LOOMPLANE_FALKORDB_GRAPH_COUNT"))
	if err != nil || graphErr != nil || expectedGraphs < 0 || expectedGraphs > 1 {
		t.Fatal("fixture expected limit or graph count is invalid")
	}
	before := snapshotFalkorNativeState(t, ctx, fixture.Default, ports[TLSDisable], expectedGraphs)
	if before.limit != expectedLimit {
		t.Fatalf("fixture RESULTSET_SIZE differs (got=%d, want=%d)", before.limit, expectedLimit)
	}
	for _, tc := range []struct {
		name, raw, mode, problem string
		readsConfig              bool
	}{
		{"GET-only plaintext", fixture.Plain, TLSDisable, "", true},
		{"GET-only verified TLS", fixture.Verified, TLSVerifyFull, "", true},
		{"default user", fixture.Default, TLSDisable, "", true},
		{"GET denied", fixture.Denied, TLSDisable, "the FalkorDB configuration read was denied", true},
		{"recovery app", fixture.Recovery, TLSDisable, "the FalkorDB configuration read was denied", true},
		{"wrong password", fixture.Plain, TLSDisable, "the FalkorDB credentials were refused", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := ParseEngineAccess(tc.raw)
			port := strconv.Itoa(a.Port)
			if err != nil || a.Engine != EngineFalkorDB || a.Host != "127.0.0.1" || a.TLS != tc.mode || port != ports[a.TLS] {
				t.Fatal("access must match the fixture's direct published port and TLS mode")
			}
			if tc.name == "wrong password" {
				a.Password += "-wrong-fixture-password"
			}
			raw, err := EncodeEngineAccess(a)
			if err != nil {
				t.Fatal("could not encode FalkorDB fixture access")
			}
			authority := net.JoinHostPort(a.Host, port)
			calls := 0
			var transcript *falkorNativeCommandConn
			dial := func(ctx context.Context, network, address string) (net.Conn, error) {
				calls++
				if calls != 1 || network != "tcp" || address != authority {
					return nil, errors.New("unexpected fixture dial")
				}
				conn, dialErr := (&net.Dialer{}).DialContext(ctx, network, address)
				if dialErr != nil || a.TLS != TLSDisable {
					return conn, dialErr
				}
				want := []string{falkorTestArray(falkorTestBulk("HELLO"), falkorTestBulk("2"), falkorTestBulk("AUTH"), falkorTestBulk(a.Username), falkorTestBulk(a.Password))}
				if tc.readsConfig {
					want = append(want, falkorTestArray(falkorTestBulk("GRAPH.CONFIG"), falkorTestBulk("GET"), falkorTestBulk("RESULTSET_SIZE")))
				}
				transcript = &falkorNativeCommandConn{Conn: conn, pending: want}
				return transcript, nil
			}
			report, err := (FalkorDBProber{}).Probe(ctx, dial, raw, recipes.Requirement{Engine: EngineFalkorDB, MinVersion: "4.2.2", TLS: []string{a.TLS}})
			if calls != 1 || a.TLS == TLSDisable && (transcript == nil || len(transcript.pending) != 0) {
				t.Fatal("unexpected FalkorDB native command sequence")
			}
			if tc.problem != "" {
				if err == nil || err.Error() != tc.problem || report != nil {
					t.Fatal("unexpected FalkorDB native failure classification")
				}
			} else if err != nil || report == nil || report.Version != "4.2.2" || !report.Result().OK {
				t.Fatal("FalkorDB native check failed")
			}
		})
	}
	after := snapshotFalkorNativeState(t, ctx, fixture.Default, ports[TLSDisable], expectedGraphs)
	if before != after {
		t.Fatal("external checks changed fixture config, key count, graph or telemetry data")
	}

}

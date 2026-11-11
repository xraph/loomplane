//go:build qualification

package catalogconnect

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

// TestQualifyExternalSearchNative checks an explicitly supplied, running pinned
// container. It does not start containers, install roles or change native data.
func TestQualifyExternalSearchNative(t *testing.T) {
	if os.Getenv("LOOMPLANE_SEARCH_NATIVE") != "1" {
		t.Skip("explicit native search qualification required")
	}
	socket, container, architecture := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET"), os.Getenv("LOOMPLANE_SEARCH_CONTAINER"), os.Getenv("LOOMPLANE_ENGINE_ARCH")
	if !filepath.IsAbs(socket) || container == "" || architecture != "amd64" && architecture != "arm64" {
		t.Fatal("explicit local socket, container and architecture required")
	}
	a, err := ParseEngineAccess(os.Getenv("LOOMPLANE_SEARCH_ACCESS"))
	if err != nil {
		t.Fatal("valid write-only search access required")
	}
	target := os.Getenv("LOOMPLANE_SEARCH_NATIVE_ADDRESS")
	host, port, err := net.SplitHostPort(target)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("explicit loopback native port required")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		t.Fatal("invalid native port")
	}
	kind, ok := recipes.Default().Kind(a.Engine)
	if !ok || len(kind.Releases) != 1 {
		t.Fatal("pinned search recipe required")
	}
	release := kind.Releases[0]
	image := release.Images[a.Engine]
	digest := image.Digests[architecture]
	if digest == "" {
		t.Fatal("architecture pin missing")
	}
	reference := image.Repository + "@" + digest
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	inspect := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...)
		data, err := cmd.Output()
		if err != nil {
			t.Fatal("native container metadata could not be verified")
		}
		return data
	}
	var actual struct {
		Image           string
		State           struct{ Running bool }
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string
			}
		}
	}
	// Select only the metadata needed to verify the image and published port.
	metadata := inspect("container", "inspect", "--format", `{"Image":{{json .Image}},"State":{"Running":{{json .State.Running}}},"NetworkSettings":{"Ports":{{json .NetworkSettings.Ports}}}}`, container)
	if json.Unmarshal(metadata, &actual) != nil || !actual.State.Running {
		t.Fatal("native search container is not running")
	}
	var pinned struct{ ID, Architecture string }
	if json.Unmarshal(inspect("image", "inspect", "--format", `{"ID":{{json .Id}},"Architecture":{{json .Architecture}}}`, reference), &pinned) != nil || pinned.ID != actual.Image || pinned.Architecture != architecture {
		t.Fatal("native container does not match selected recipe image and architecture")
	}
	direct := false
	for _, published := range actual.NetworkSettings.Ports["9200/tcp"] {
		if published.HostIP == "127.0.0.1" && published.HostPort == port {
			direct = true
		}
	}
	if !direct {
		t.Fatal("native target must be the selected container's directly published loopback port")
	}
	raw, err := EncodeEngineAccess(a)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		if network != "tcp" || address != net.JoinHostPort(a.Host, strconv.Itoa(a.Port)) {
			return nil, errors.New("native prober changed authority")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	}
	report, err := (SearchProber{}).Probe(ctx, dial, raw, recipes.Requirement{Engine: a.Engine, TLS: []string{a.TLS}})
	if err != nil || report == nil || !report.Result().OK || report.Version != release.Product || calls.Load() != 3 {
		t.Fatal("pinned native search identity, authentication and health read did not pass")
	}
	bad := a
	bad.Password += "-wrong"
	badRaw, err := EncodeEngineAccess(bad)
	if err != nil {
		t.Fatal("wrong-password search access could not be prepared")
	}
	report, err = (SearchProber{}).Probe(ctx, dial, badRaw, recipes.Requirement{Engine: a.Engine})
	if err != nil && strings.Contains(err.Error(), a.Password) {
		t.Fatal("native failure exposed a credential")
	}
	if !nativeSearchAuthRefused(report, err) {
		t.Fatal("wrong-password probe did not return a credential or read-permission refusal")
	}
	t.Logf("verified engine=%s architecture=%s product=%s authenticated_identity=true cluster_health_read=true", a.Engine, architecture, release.Product)
}

func nativeSearchAuthRefused(report *Report, err error) bool {
	var refusal *probeError
	return report == nil && errors.As(err, &refusal) && refusal != nil && refusal.answered &&
		refusal.msg == "the search application credentials or required read permission were refused"
}

func TestSearchQualificationRequiresAuthenticationRefusal(t *testing.T) {
	access := searchAccess(EngineElasticsearch)
	raw, err := EncodeEngineAccess(access)
	if err != nil {
		t.Fatal("qualification regression access could not be prepared")
	}
	requirement := recipes.Requirement{Engine: access.Engine}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: true},
		{name: "forbidden", status: http.StatusForbidden, want: true},
		{name: "server failure", status: http.StatusInternalServerError},
		{name: "wrong product", status: http.StatusOK, body: openSearchRoot},
		{name: "malformed response", status: http.StatusOK, body: "incomplete"},
		{name: "unexpected success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			var mu sync.Mutex
			handler := searchNativeHandler(access.Engine, &requests, &mu)
			if tc.status != 0 {
				handler = func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}
			}
			_, _, dial, _ := searchTestServer(t, access.Engine, handler)
			report, probeErr := (SearchProber{}).Probe(t.Context(), dial, raw, requirement)
			if tc.status == 0 {
				if probeErr != nil || report == nil || !report.Result().OK {
					t.Fatal("expected a successful probe in the acceptance control")
				}
			} else if probeErr == nil {
				t.Fatal("expected a refusal or response error from the probe")
			}
			if nativeSearchAuthRefused(report, probeErr) != tc.want {
				t.Fatal("qualification classified the wrong probe outcome as authentication refusal")
			}
		})
	}

	expired, stopDeadline := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer stopDeadline()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "dial failure", ctx: t.Context()},
		{name: "deadline", ctx: expired},
		{name: "canceled", ctx: canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dial := func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("fixture dial failed")
			}
			report, probeErr := (SearchProber{}).Probe(tc.ctx, dial, raw, requirement)
			if probeErr == nil || nativeSearchAuthRefused(report, probeErr) {
				t.Fatal("qualification counted a failed connection as authentication refusal")
			}
		})
	}
}

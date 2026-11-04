package server

import (
	"io"
	"net"
	"net/http"
	"slices"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// podmanDaemon answers like Podman's Docker-compatible API, whose /info
// can leave out fields Docker always sends: the daemon ID and the runtimes.
func podmanDaemon(info string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, `{"Version":"5.4.2","APIVersion":"1.41","Os":"linux","Arch":"amd64"}`)
		case "/info":
			_, _ = io.WriteString(w, info)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func podmanClient(t *testing.T, info string) *Client {
	t.Helper()
	daemon := fakeDaemon(t, podmanDaemon(info))
	return nodeClient(t, &nodeStub{t: t, serve: func(_ nodeproto.StreamHeader, conn net.Conn) { proxyTo(t, daemon, conn) }}, "")
}

func TestPodmanInfoWithoutIDMissingIdentity(t *testing.T) {
	c := podmanClient(t, `{"OSType":"linux","Architecture":"x86_64"}`)
	details, missing, err := c.Check(t.Context())
	if err != nil || !slices.Contains(missing, "docker:identity") || details["docker_id"] != "" {
		t.Fatalf("a daemon without an ID passed: %v %v %v", details, missing, err)
	}
	if slices.Contains(missing, "docker:runtimes") {
		t.Fatalf("no GPUs were requested, yet the runtimes were: %v", missing)
	}

	// Asked to run GPU work, a daemon that names no runtimes can't say it
	// has NVIDIA's, so the check names that too.
	c.RequestsGPUs = true
	if _, missing, err = c.Check(t.Context()); err != nil || !slices.Contains(missing, "docker:runtimes") {
		t.Fatalf("missing runtimes passed a GPU check: %v %v", missing, err)
	}

	c = podmanClient(t, `{"ID":"podman-host","OSType":"linux","Architecture":"x86_64","Runtimes":{"crun":{"path":"/usr/bin/crun"}}}`)
	c.RequestsGPUs = true
	if details, missing, err = c.Check(t.Context()); err != nil || len(missing) != 0 || details["docker_id"] != "podman-host" || details["architecture"] != "amd64" {
		t.Fatalf("a complete daemon failed its check: %v %v %v", details, missing, err)
	}
}

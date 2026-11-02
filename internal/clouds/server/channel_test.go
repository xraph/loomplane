package server

import (
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// inspectDocker answers container inspections with the ports each
// container publishes, as Docker reports them.
type inspectDocker struct {
	mu        sync.Mutex
	ports     map[string]string
	inspected []string
}

func (d *inspectDocker) RoundTrip(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inspected = append(d.inspected, r.Method+" "+r.URL.Path)
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
	ports, ok := d.ports[name]
	if r.Method != http.MethodGet || !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"message":"No such container"}`)), Header: http.Header{}, Request: r}, nil
	}
	body := `{"Name":"/` + name + `","NetworkSettings":{"Ports":` + ports + `}}`
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}, Request: r}, nil
}

func TestDialContainerPortUsesLoopbackBinding(t *testing.T) {
	cfg, _ := sshFixture(t)
	upstream, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			_, _ = io.WriteString(conn, "admin api")
			_ = conn.Close()
		}
	}()
	_, published, err := net.SplitHostPort(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c, err := Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	docker := &inspectDocker{ports: map[string]string{
		"lp-control": `{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"49169"}],"9090/tcp":[{"HostIp":"127.0.0.1","HostPort":"` + published + `"}]}`,
		"lp-public":  `{"9090/tcp":[{"HostIp":"0.0.0.0","HostPort":"49171"}]}`,
		"lp-both":    `{"9090/tcp":[{"HostIp":"127.0.0.1","HostPort":"` + published + `"},{"HostIp":"::","HostPort":"` + published + `"}]}`,
		"lp-private": `{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"49169"}]}`,
		"lp-stopped": `{"9090/tcp":null}`,
	}}
	c.HTTP = &http.Client{Transport: docker}

	conn, err := c.DialContainerPort(t.Context(), "lp-control", 9090)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	_ = conn.Close()
	if err != nil || string(got) != "admin api" {
		t.Fatalf("read %q: %v", got, err)
	}
	if len(docker.inspected) != 1 || docker.inspected[0] != "GET /containers/lp-control/json" {
		t.Fatalf("inspected %v", docker.inspected)
	}

	for _, name := range []string{"lp-public", "lp-both"} {
		if conn, err := c.DialContainerPort(t.Context(), name, 9090); err == nil || err.Error() != "port 9090 is published beyond loopback" {
			if conn != nil {
				_ = conn.Close()
			}
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"lp-private", "lp-stopped", "lp-gone"} {
		if conn, err := c.DialContainerPort(t.Context(), name, 9090); err == nil {
			_ = conn.Close()
			t.Fatalf("%s: dialed a port the container doesn't publish", name)
		}
	}
	for _, port := range []int{0, 70000} {
		if conn, err := c.DialContainerPort(t.Context(), "lp-control", port); err == nil {
			_ = conn.Close()
			t.Fatalf("dialed port %d", port)
		}
	}
	if conn, err := c.DialContainerPort(t.Context(), "../images", 9090); err == nil {
		_ = conn.Close()
		t.Fatal("dialed a container name that isn't one")
	}
}

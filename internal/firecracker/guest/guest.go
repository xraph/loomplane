// Package guest runs inside the controlled Firecracker guest image only.
package guest

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

const buildkitImage = "docker.io/moby/buildkit:v0.34.1-rootless@sha256:f68752a5a94acfcdad2d07867b7075601a421638e17fc5f28efc1c814d0c4f89"

type Service struct {
	Root      string
	network   fc.NetworkValues
	networkMu sync.RWMutex
	mu        sync.Mutex
	token, vm string
	request   *fc.Request
	status    fc.GuestStatus
	output    boundedOutput
	running   bool
}
type boundedOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n >= fc.MaxLogBytes {
		b.data = append(b.data[:0], p[n-fc.MaxLogBytes:]...)
	} else {
		b.data = append(b.data, p...)
		if len(b.data) > fc.MaxLogBytes {
			b.data = append(b.data[:0], b.data[len(b.data)-fc.MaxLogBytes:]...)
		}
	}
	return n, nil
}
func (b *boundedOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	vm := r.Header.Get("Loomplane-VM")
	s.mu.Lock()
	if s.token != "" && (subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 || vm != s.vm) {
		s.mu.Unlock()
		http.Error(w, "identity", http.StatusForbidden)
		return
	}
	if s.token == "" && r.URL.Path != "/configure" {
		s.mu.Unlock()
		http.Error(w, "not configured", http.StatusConflict)
		return
	}
	s.mu.Unlock()
	var out fc.GuestStatus
	var err error
	switch r.URL.Path {
	case "/peers":
		var network fc.NetworkValues
		err = fc.Decode(http.MaxBytesReader(w, r.Body, fc.MaxControlBytes), &network, fc.MaxControlBytes)
		if err == nil && !validNetwork(network) {
			err = fc.ErrInvalid
		}
		if err == nil {
			s.mu.Lock()
			s.networkMu.Lock()
			s.network = network
			s.networkMu.Unlock()
			s.mu.Unlock()
			out.Ready = true
		}
	case "/configure":
		var q fc.Request
		err = fc.Decode(http.MaxBytesReader(w, r.Body, fc.MaxControlBytes), &q, fc.MaxControlBytes)
		if err == nil {
			out, err = s.configure(r.Context(), token, vm, q)
		}
	case "/source":
		out, err = s.source(r.Context(), http.MaxBytesReader(w, r.Body, fc.MaxSourceBytes))
	case "/status", "/logs":
		out, err = s.observe(r.Context())
	case "/tunnel":
		s.tunnel(w, r)
		return
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "guest operation failed", http.StatusConflict)
		return
	}
	fc.WriteJSON(w, http.StatusOK, out)
}
func (s *Service) configure(ctx context.Context, token, vm string, q fc.Request) (fc.GuestStatus, error) {
	if len(token) != 64 || len(vm) != 32 || !filepath.IsAbs(s.Root) {
		return fc.GuestStatus{}, fc.ErrInvalid
	}
	if err := q.Validate(time.Now()); err != nil {
		return fc.GuestStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.request != nil {
		if !reflect.DeepEqual(s.request.Spec, q.Spec) || s.request.Scope != q.Scope {
			return s.status, fc.ErrConflict
		}
		if s.status.Ready {
			return s.status, nil
		}
	}
	if !validNetwork(q.Network) {
		return s.status, fc.ErrInvalid
	}
	s.networkMu.Lock()
	s.network = q.Network
	s.networkMu.Unlock()
	s.token, s.vm = token, vm
	s.request = &q
	s.status.State = "starting"
	if err := os.MkdirAll(filepath.Join(s.Root, "config"), 0o700); err != nil {
		return s.status, err
	}
	registryConfig := q.Values.RegistryConfig
	if len(registryConfig) == 0 {
		registryConfig = []byte("{}")
	}
	if err := os.WriteFile(filepath.Join(s.Root, "config", "config.json"), registryConfig, 0o600); err != nil {
		return s.status, err
	}
	var env strings.Builder
	keys := make([]string, 0, len(q.Values.Env))
	for k := range q.Values.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if strings.ContainsAny(q.Values.Env[k], "\r\n") {
			return s.status, fc.ErrInvalid
		}
		env.WriteString(k + "=" + q.Values.Env[k] + "\n")
	}
	envPath := filepath.Join(s.Root, "env")
	if err := os.WriteFile(envPath, []byte(env.String()), 0o600); err != nil {
		return s.status, err
	}
	if q.Spec.Kind == "build" {
		s.status.State = "awaiting-source"
		s.status.Ready = true
		return s.status, nil
	}
	_ = s.run(ctx, "rm", "--force", "workload") //nolint:errcheck // A fresh scratch disk has no prior container.
	if err := s.run(ctx, appArgs(q.Spec, envPath)...); err != nil {
		s.status.State = "failed"
		return s.status, err
	}
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		ready, err := s.appReady(ctx)
		if err == nil && ready {
			s.status.State = "running"
			s.status.Ready = true
			return s.status, nil
		}
		select {
		case <-ctx.Done():
			return s.status, ctx.Err()
		case <-deadline.C:
			return s.status, errors.New("guest readiness deadline exceeded")
		case <-tick.C:
		}
	}
}
func appArgs(spec fc.Spec, env string) []string {
	args := []string{"run", "--detach", "--name", "workload", "--net", "host", "--env-file", env, "--log-driver", "json-file", "--log-opt", "max-size=1m", "--log-opt", "max-file=2"}
	if len(spec.Command) > 0 {
		args = append(args, "--entrypoint", spec.Command[0])
	}
	args = append(args, spec.Image)
	if len(spec.Command) > 1 {
		args = append(args, spec.Command[1:]...)
	}
	return append(args, spec.Args...)
}
func (s *Service) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "nerdctl", append([]string{"--data-root", "/scratch/nerdctl"}, args...)...) //nolint:gosec // Structured arguments launch an OCI runtime inside the guest, never a host shell.
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+filepath.Join(s.Root, "config"))
	cmd.Stdout = &s.output
	cmd.Stderr = &s.output
	return cmd.Run()
}
func (s *Service) appReady(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "nerdctl", "--data-root", "/scratch/nerdctl", "inspect", "--format", "{{.State.Running}}", "workload") //nolint:gosec // All arguments are literals; this command runs inside the VM.
	raw, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(raw)) != "true" {
		return false, err
	}
	if s.request.Spec.Port == 0 {
		return true, nil
	}
	addr := net.JoinHostPort("127.0.0.1", itoa(s.request.Spec.Port))
	if s.request.Spec.HealthPath == "" {
		c, e := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if e != nil {
			return false, e
		}
		_ = c.Close()
		return true, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+s.request.Spec.HealthPath, http.NoBody)
	if err != nil {
		return false, err
	}
	resp, err := (&http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400, nil
}
func (s *Service) observe(ctx context.Context) (fc.GuestStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.request == nil {
		return s.status, fc.ErrConflict
	}
	if s.request.Spec.Kind == "app" {
		ready, probeErr := s.appReady(ctx)
		if probeErr != nil {
			ready = false
		}
		s.status.Ready = ready
		if !ready {
			s.status.State = "failed"
		} else {
			s.status.State = "running"
		}
		_ = s.run(ctx, "logs", "--tail", "100", "workload") //nolint:errcheck // A missing log does not change the independently probed process status.
	}
	out := s.status
	out.Output = s.redact(s.output.String())
	return out, nil
}
func (s *Service) redact(output string) string {
	var values []string
	for _, v := range s.request.Values.Env {
		if len(v) > 3 {
			values = append(values, v)
		}
	}
	var config map[string]any
	if json.Unmarshal(s.request.Values.RegistryConfig, &config) == nil {
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				for _, v := range x {
					walk(v)
				}
			case string:
				if len(x) > 3 {
					values = append(values, x)
					if raw, e := base64.StdEncoding.DecodeString(x); e == nil {
						values = append(values, string(raw))
						if _, password, ok := strings.Cut(string(raw), ":"); ok && len(password) > 3 {
							values = append(values, password)
						}
					}
				}
			}
		}
		walk(config)
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	for _, v := range values {
		output = strings.ReplaceAll(output, v, "[redacted]")
	}
	return output
}

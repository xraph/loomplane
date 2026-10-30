package guest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

func itoa(v int) string { return strconv.Itoa(v) }
func (s *Service) source(ctx context.Context, body io.Reader) (fc.GuestStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.request == nil || s.request.Spec.Kind != "build" {
		return s.status, fc.ErrInvalid
	}
	if s.running || s.status.State == "succeeded" {
		return s.status, nil
	}
	source := "/scratch/source"
	if err := os.Mkdir(source, 0o755); err != nil { //nolint:gosec // Source must be readable by uid 1000 inside this disposable guest.
		return s.status, err
	}
	digest := sha256.New()
	bounded := &io.LimitedReader{R: body, N: fc.MaxSourceBytes + 1}
	stream := io.TeeReader(bounded, digest)
	if err := extract(source, stream); err != nil {
		return s.status, err
	}
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return s.status, err
	}
	if bounded.N <= 0 || hex.EncodeToString(digest.Sum(nil)) != s.request.Spec.SourceDigest {
		return s.status, fc.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return s.status, err
	}
	result := "/scratch/result"
	if err := os.Mkdir(result, 0o755); err != nil { //nolint:gosec // The rootless builder owns this directory inside its VM.
		return s.status, err
	}
	// The rootless BuildKit image runs as uid 1000 inside this disposable VM.
	if err := os.Chown(result, 1000, 1000); err != nil {
		return s.status, err
	}
	registry := filepath.Join(s.Root, "config")
	if err := os.Chmod(registry, 0o755); err != nil { //nolint:gosec // Directory traversal permits the builder uid; config.json remains mode 0600.
		return s.status, err
	}
	if err := os.Chown(filepath.Join(registry, "config.json"), 1000, 1000); err != nil {
		return s.status, err
	}
	s.running = true
	s.status.State = "building"
	go func() { //nolint:gosec // Upload completion must not cancel the build; its own deadline and host lifetime bound it.
		run, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := s.run(run, "run", "--rm", "--name", "build", "--net", "host", "--security-opt", "seccomp=unconfined", "--security-opt", "apparmor=unconfined", "--mount", "type=bind,src="+source+",dst=/workspace,ro", "--mount", "type=bind,src="+registry+",dst=/registry,ro", "--mount", "type=bind,src="+result+",dst=/result", "--env", "DOCKER_CONFIG=/registry", "--env", "BUILDKITD_FLAGS=--oci-worker-no-process-sandbox --oci-worker-snapshotter=native", "--entrypoint", "buildctl-daemonless.sh", buildkitImage, "build", "--frontend", "dockerfile.v0", "--local", "context=/workspace", "--local", "dockerfile=/workspace", "--opt", "filename=.loomplane-build/Dockerfile", "--output", "type=image,name="+s.request.Spec.BuildImage+",push=true,oci-mediatypes=true", "--metadata-file", "/result/metadata.json", "--progress", "plain")
		s.mu.Lock()
		defer s.mu.Unlock()
		s.running = false
		s.status.State = "failed"
		s.status.ExitCode = 1
		if err == nil {
			raw, e := os.ReadFile(filepath.Join(result, "metadata.json"))
			if e == nil {
				digest, parseErr := imageDigest(raw)
				if parseErr == nil {
					s.status.Digest = digest
					s.status.ExitCode = 0
					s.status.State = "succeeded"
				}
			}
		}
	}()
	return s.status, nil
}
func (s *Service) tunnel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.request == nil || s.request.Spec.Kind != "app" || s.request.Spec.Port == 0 {
		s.mu.Unlock()
		http.Error(w, "no service port", http.StatusConflict)
		return
	}
	port := s.request.Spec.Port
	s.mu.Unlock()
	target, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(r.Context(), "tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	defer target.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "transport", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err = buf.WriteString("HTTP/1.1 200 OK\r\n\r\n"); err != nil {
		return
	}
	if err = buf.Flush(); err != nil {
		return
	}
	if err = conn.SetDeadline(time.Now().Add(10 * time.Minute)); err != nil {
		return
	}
	if err = target.SetDeadline(time.Now().Add(10 * time.Minute)); err != nil {
		return
	}
	done := make(chan error, 1)
	go func() { _, e := io.Copy(target, buf); done <- e }()
	_, _ = io.Copy(conn, target) //nolint:errcheck // Closing either peer ends the tunnel; there is no second response.
	_ = target.Close()
	_ = conn.Close()
	<-done
}

func imageDigest(raw []byte) (string, error) {
	var metadata struct {
		Digest string `json:"containerimage.digest"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(metadata.Digest) {
		return "", fc.ErrInvalid
	}
	return metadata.Digest, nil
}

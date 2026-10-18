// This acceptance binary runs inside its disposable, network-isolated container.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	agentv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/agent/v1"
	bundlev1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/bundle/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type registry struct {
	identity domain.AgentIdentity
	active   bool
}

func (r *registry) CheckAgent(_ context.Context, a domain.AgentIdentity) error {
	if !r.active || r.identity != a {
		return workloadidentity.ErrIdentity
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	if err := os.MkdirAll("/tmp", 0o700); err != nil {
		return err
	}
	dir, runErr := os.MkdirTemp("/tmp", "loomplane-identity-")
	if runErr != nil {
		return runErr
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, "fixture cleanup:", err)
		}
	}()
	serverSocket := filepath.Join(dir, "server.sock")
	serverConfig := fmt.Sprintf(`server {
 bind_address = "127.0.0.1"
 bind_port = "8081"
 socket_path = %q
 trust_domain = "loomplane.test"
 data_dir = %q
 log_level = "ERROR"
}
plugins {
 DataStore "sql" { plugin_data { database_type = "sqlite3" connection_string = %q } }
 NodeAttestor "join_token" { plugin_data {} }
 KeyManager "memory" { plugin_data {} }
}
`, serverSocket, filepath.Join(dir, "server"), filepath.Join(dir, "database.sqlite3"))
	serverPath := filepath.Join(dir, "server.conf")
	if err := os.WriteFile(serverPath, []byte(serverConfig), 0o600); err != nil {
		return err
	}
	stopServer, runErr := start(ctx, dir, "server", "/opt/spire/bin/spire-server", "run", "-config", serverPath)
	if runErr != nil {
		return runErr
	}
	defer stopServer()
	// Administrative APIs are reachable only through this fixture's Unix socket.
	conn, runErr := grpc.NewClient("unix://"+serverSocket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if runErr != nil {
		return runErr
	}
	defer func() {
		if err := conn.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	bundleClient := bundlev1.NewBundleClient(conn)
	var bundle *spiretypes.Bundle
	if err := until(ctx, func() bool {
		bundle, runErr = bundleClient.GetBundle(ctx, &bundlev1.GetBundleRequest{})
		return runErr == nil
	}); err != nil {
		return fmt.Errorf("SPIRE server readiness: %w", err)
	}
	roots := []*x509.Certificate{}
	var rootPEM []byte
	for _, ca := range bundle.X509Authorities {
		if ca.Tainted {
			continue
		}
		cert, err := x509.ParseCertificate(ca.Asn1)
		if err != nil {
			return err
		}
		roots = append(roots, cert)
		rootPEM = append(rootPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Asn1})...)
	}
	rootPath := filepath.Join(dir, "roots.pem")
	if err := os.WriteFile(rootPath, rootPEM, 0o600); err != nil {
		return err
	}
	join, runErr := agentv1.NewAgentClient(conn).CreateJoinToken(ctx, &agentv1.CreateJoinTokenRequest{Ttl: 90, AgentId: &spiretypes.SPIFFEID{TrustDomain: "loomplane.test", Path: "/node/fixture"}})
	if runErr != nil {
		return runErr
	}
	config := func(name string) (string, error) {
		data := filepath.Join(dir, name)
		if err := os.MkdirAll(data, 0o700); err != nil {
			return "", err
		}
		body := fmt.Sprintf(`agent {
 data_dir = %q
 log_level = "DEBUG"
 server_address = "127.0.0.1"
 server_port = "8081"
 socket_path = %q
 trust_bundle_path = %q
 trust_domain = "loomplane.test"
}
plugins {
 NodeAttestor "join_token" { plugin_data {} }
 KeyManager "disk" { plugin_data { directory = %q } }
 WorkloadAttestor "unix" { plugin_data { discover_workload_path = true workload_size_limit = 134217728 } }
}
`, data, filepath.Join(data, "agent.sock"), rootPath, data)
		path := filepath.Join(data, "agent.conf")
		return path, os.WriteFile(path, []byte(body), 0o600)
	}
	agentPath, runErr := config("agent")
	if runErr != nil {
		return runErr
	}
	stopAgent, runErr := start(ctx, dir, "agent", "/opt/spire/bin/spire-agent", "run", "-config", agentPath, "-joinToken", join.Value)
	if runErr != nil {
		return runErr
	}
	defer stopAgent()
	a := domain.AgentIdentity{Scope: domain.Scope{OrgID: "fixture", ProjectID: "project", EnvironmentID: "test"}, AgentID: "agent", DestinationID: "destination", Epoch: 1}
	a.URI, runErr = workloadidentity.AgentURI("loomplane.test", a)
	if runErr != nil {
		return runErr
	}
	executable, runErr := os.Executable()
	if runErr != nil {
		return runErr
	}
	raw, runErr := os.ReadFile(executable)
	if runErr != nil {
		return runErr
	}
	digest := sha256.Sum256(raw)
	uid := uint32(0)
	issuer, runErr := workloadidentity.NewSPIREIssuer(conn, "loomplane.test")
	if runErr != nil {
		return runErr
	}
	binding := workloadidentity.Binding{EntryID: "01234567-89ab-cdef-0123-456789abcdef", Agent: a, Destination: domain.Destination{Scope: a.Scope, ID: a.DestinationID, Generation: 1, Identity: domain.PhysicalIdentity{Provider: "server", Account: "fixture", ResourceID: "isolated-container"}}, ParentURI: "spiffe://loomplane.test/node/fixture", CheckedAt: time.Now(), Method: "unix", UnixUID: &uid, Executable: executable, SHA256: hex.EncodeToString(digest[:]), TTL: 30 * time.Second}
	if _, err := issuer.Register(ctx, binding); err != nil {
		return fmt.Errorf("SPIRE registration: %w", err)
	}
	sourceCtx, sourceCancel := context.WithTimeout(ctx, 30*time.Second)
	defer sourceCancel()
	source, runErr := workloadidentity.OpenSource(sourceCtx, "unix://"+filepath.Join(dir, "agent", "agent.sock"), a.URI)
	if runErr != nil {
		return fmt.Errorf("attested Workload API: %w", runErr)
	}
	defer func() {
		if err := source.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	first, runErr := source.GetX509SVID()
	if runErr != nil {
		return runErr
	}
	trust, runErr := workloadidentity.NewTrust("loomplane.test", roots)
	if runErr != nil {
		return runErr
	}
	registered := &registry{identity: a, active: true}
	verifier := workloadidentity.NewVerifier(trust, registered)
	if _, err := verifier.VerifyPeer(ctx, first.Certificates, "loomplane.test"); err != nil {
		return err
	}
	fmt.Println("PASS attested Unix identity through pinned SPIRE runtime and Workload API")
	if _, err := verifier.VerifyPeer(ctx, first.Certificates, "foreign.test"); err == nil {
		return errors.New("foreign trust domain accepted live credential")
	}
	registered.identity.DestinationID = "neighbor"
	if _, err := verifier.VerifyPeer(ctx, first.Certificates, "loomplane.test"); err == nil {
		return errors.New("wrong destination accepted live credential")
	}
	registered.identity = a
	unattested := binding
	unattested.EntryID = "11234567-89ab-cdef-0123-456789abcdef"
	unattested.Agent.AgentID = "unattested"
	unattested.Agent.URI, runErr = workloadidentity.AgentURI("loomplane.test", unattested.Agent)
	if runErr != nil {
		return runErr
	}
	unattested.SHA256 = strings.Repeat("0", 64)
	if _, err := issuer.Register(ctx, unattested); err != nil {
		return err
	}
	denialCtx, denialCancel := context.WithTimeout(ctx, 10*time.Second)
	unattestedSource, denialErr := workloadidentity.OpenSource(denialCtx, "unix://"+filepath.Join(dir, "agent", "agent.sock"), unattested.Agent.URI)
	denialCancel()
	if denialErr == nil {
		return errors.Join(errors.New("unattested executable obtained an identity"), unattestedSource.Close())
	}
	if !strings.Contains(denialErr.Error(), "missing X509-SVID") {
		return fmt.Errorf("attestation denial was not proven: %w", denialErr)
	}
	if err := issuer.Revoke(ctx, unattested.EntryID); err != nil {
		return err
	}
	fmt.Println("PASS wrong trust domain, wrong destination and missing executable attestation are denied")

	// Same bootstrap token in an empty agent directory cannot attest a second node.
	replayPath, runErr := config("replay")
	if runErr != nil {
		return runErr
	}
	stopReplay, runErr := start(ctx, dir, "replay", "/opt/spire/bin/spire-agent", "run", "-config", replayPath, "-joinToken", join.Value)
	if runErr != nil {
		return runErr
	}
	replayCtx, replayCancel := context.WithTimeout(ctx, 5*time.Second)
	replay, replayErr := workloadidentity.OpenSource(replayCtx, "unix://"+filepath.Join(dir, "replay", "agent.sock"), a.URI)
	replayCancel()
	stopReplay()
	if replayErr == nil {
		closeErr := replay.Close()
		return errors.Join(errors.New("replayed join token issued identity"), closeErr)
	}
	replayLog, readErr := os.ReadFile(filepath.Join(dir, "replay.log"))
	if readErr != nil {
		return readErr
	}
	reason := strings.ToLower(string(replayLog))
	if !strings.Contains(reason, "token") || (!strings.Contains(reason, "not found") && !strings.Contains(reason, "does not exist") && !strings.Contains(reason, "already been used") && !strings.Contains(reason, "already used")) {
		return fmt.Errorf("replay rejection was not proven: %s", strings.ReplaceAll(string(replayLog), join.Value, "[REDACTED]"))
	}
	fmt.Println("PASS consumed join token cannot attest a second fresh agent")
	originalSerial := first.Certificates[0].SerialNumber.String()
	if err := until(ctx, func() bool {
		svid, err := source.GetX509SVID()
		return err == nil && svid.Certificates[0].SerialNumber.String() != originalSerial
	}); err != nil {
		return fmt.Errorf("SVID rotation: %w", err)
	}
	fmt.Println("PASS Workload API rotates the live credential")
	if err := until(ctx, func() bool { return time.Now().After(first.Certificates[0].NotAfter) }); err != nil {
		return err
	}
	if _, err := verifier.VerifyPeer(ctx, first.Certificates, "loomplane.test"); err == nil {
		return errors.New("expired live SVID accepted")
	}
	fresh, runErr := source.GetX509SVID()
	if runErr != nil {
		return runErr
	}
	if _, err := verifier.VerifyPeer(ctx, fresh.Certificates, "loomplane.test"); err != nil {
		return err
	}
	fmt.Println("PASS expired credential denied while rotated credential remains valid")
	if err := qualifyRuntime(ctx, conn, source, binding, "unix://"+filepath.Join(dir, "agent", "agent.sock")); err != nil {
		return err
	}
	registered.active = false
	if _, err := verifier.VerifyPeer(ctx, fresh.Certificates, "loomplane.test"); err == nil {
		return errors.New("revoked live identity accepted")
	}
	if err := issuer.Revoke(ctx, binding.EntryID); err != nil {
		return err
	}
	fmt.Println("PASS revoked registry entry denies an otherwise valid live credential")
	return nil
}
func until(ctx context.Context, ready func() bool) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func start(ctx context.Context, dir, name, executable string, args ...string) (func(), error) {
	processCtx, cancel := context.WithCancel(ctx)
	log, err := os.OpenFile(filepath.Join(dir, name+".log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		cancel()
		return nil, err
	}
	var command *exec.Cmd
	switch executable {
	case "/opt/spire/bin/spire-server":
		command = exec.CommandContext(processCtx, "/opt/spire/bin/spire-server", args...) //nolint:gosec // G204: fixed binary and fixture-owned arguments; no shell.
	case "/opt/spire/bin/spire-agent":
		command = exec.CommandContext(processCtx, "/opt/spire/bin/spire-agent", args...) //nolint:gosec // G204: fixed binary and fixture-owned arguments; no shell.
	default:
		cancel()
		return nil, errors.Join(errors.New("unrecognized fixture executable"), log.Close())
	}
	command.Stdout = log
	command.Stderr = log
	if err := command.Start(); err != nil {
		cancel()
		return nil, errors.Join(err, log.Close())
	}
	done := make(chan struct{})
	go func() {
		if waitErr := command.Wait(); waitErr != nil && processCtx.Err() == nil && name != "replay" {
			fmt.Fprintln(os.Stderr, name+" exited:", waitErr)
		}
		close(done)
	}()
	return func() {
		cancel()
		<-done
		if err := log.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}, nil
}

//go:build integration

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const pebbleImage = "ghcr.io/letsencrypt/pebble:latest@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"
const challengeImage = "ghcr.io/letsencrypt/pebble-challtestsrv:latest@sha256:12ce21884def456bcf9786542113949e1f19dc7738d2c70e156c2d0c38a1405b"

func ingressHTTPS(c *Client, address, hostname string, roots *x509.CertPool) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: hostname, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return c.ssh.DialContext(ctx, "tcp", address)
	}}}
}

func ingressApp(ctx context.Context, t *testing.T, c *Client, host, server, project string) IngressBackend {
	t.Helper()
	id := ingressDocker(t, "exec", host, "docker", "run", "-d", "--name", "app-"+project, "--label", "loomplane.org=org", "--label", "loomplane.project="+project, "--label", "loomplane.environment=env-"+project, "--label", "loomplane.instance=instance-"+project, "--label", "loomplane.revision=release-"+project, IngressImage, "sh", "-c", "mkdir -p /tmp/www && echo project-"+project+"-release-"+project+" > /tmp/www/index.html && exec caddy file-server --listen :8080 --root /tmp/www")
	b, err := ConnectIngressBackend(ctx, c, "org", server, IngressBackend{OrgID: "org", ProjectID: project, EnvironmentID: "env-" + project, Service: "web", InstanceID: "instance-" + project, ReleaseID: "release-" + project, ContainerID: id, Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIngressTwoProxyACMEHandoff(t *testing.T) {
	source, sourceHost := ingressFixture(t)
	destination, destHost := ingressFixture(t)
	network := fmt.Sprintf("lp-acme-%d", time.Now().UnixNano())
	ingressDocker(t, "network", "create", network)
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "docker", "network", "disconnect", "-f", network, sourceHost).Run()
		_ = exec.CommandContext(context.Background(), "docker", "network", "disconnect", "-f", network, destHost).Run()
		_ = exec.CommandContext(context.Background(), "docker", "network", "rm", network).Run()
	})
	ingressDocker(t, "network", "connect", network, sourceHost)
	ingressDocker(t, "network", "connect", network, destHost)
	address := func(name string) string {
		return ingressDocker(t, "inspect", "--format", "{{(index .NetworkSettings.Networks \""+network+"\").IPAddress}}", name)
	}
	sourceIP, destIP := address(sourceHost), address(destHost)
	subnet := ingressDocker(t, "network", "inspect", "--format", "{{(index .IPAM.Config 0).Subnet}}", network)
	base, _, err := net.ParseCIDR(subnet)
	if err != nil {
		t.Fatal(err)
	}
	ip := base.To4()
	ip[3] = 10
	pebbleIP := ip.String()
	ip[3] = 11
	dnsIP := ip.String()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Loomplane isolated ACME fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP(pebbleIP)}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	dir := t.TempDir()
	files := map[string][]byte{"root.pem": root, "key.pem": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}
	config, _ := json.Marshal(map[string]any{"pebble": map[string]any{"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000", "certificate": "/fixture/root.pem", "privateKey": "/fixture/key.pem", "httpPort": 80, "tlsPort": 443, "retryAfter": map[string]int{"authz": 1, "order": 1}}})
	files["config.json"] = config
	for name, raw := range files {
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	dnsName, pebbleName := network+"-dns", network+"-ca"
	for _, name := range []string{dnsName, pebbleName} {
		t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-fv", name).Run() })
	}
	ingressDocker(t, "run", "-d", "--name", dnsName, "--network", network, "--ip", dnsIP, challengeImage, "-defaultIPv6", "", "-defaultIPv4", sourceIP)
	ingressDocker(t, "run", "-d", "--name", pebbleName, "--network", network, "--ip", pebbleIP, "-e", "PEBBLE_VA_NOSLEEP=1", "-v", dir+":/fixture:ro", pebbleImage, "-config", "/fixture/config.json", "-dnsserver", dnsIP+":8053")
	t.Cleanup(func() {
		if t.Failed() {
			for _, name := range []string{pebbleName, dnsName} {
				out, _ := exec.CommandContext(context.Background(), "docker", "logs", name).CombinedOutput()
				t.Logf("%s: %s", name, out)
			}
			for _, fixture := range []struct{ host, server string }{{sourceHost, "source"}, {destHost, "destination"}} {
				out, _ := exec.CommandContext(context.Background(), "docker", "exec", fixture.host, "docker", "logs", IngressName("org", fixture.server)).CombinedOutput()
				t.Logf("%s: %s", fixture.server, out)
			}
		}
	})
	sourceCtx := WithEffectFence(t.Context(), EffectFence{Operation: "org/ingress/source", Generation: 1})
	destCtx := WithEffectFence(t.Context(), EffectFence{Operation: "org/ingress/destination", Generation: 1})
	old, err := EnsureIngress(sourceCtx, source, "org", "source")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := EnsureIngress(destCtx, destination, "org", "destination")
	if err != nil {
		t.Fatal(err)
	}
	sb := ingressApp(sourceCtx, t, source, sourceHost, "source", "source")
	db := ingressApp(destCtx, t, destination, destHost, "destination", "destination")
	hostname := "move.ingress.test"
	sc := IngressConfig{OrgID: "org", ServerID: "source", Version: "serving-source", InternalCA: true, Routes: []IngressRoute{{ID: "move", Hostname: hostname, Backend: sb}}}
	old, err = ApplyIngress(sourceCtx, source, old, sc)
	if err != nil {
		t.Fatal(err)
	}
	// The source has its own certificate. Its normal route remains untouched while
	// only HTTP-01 challenges are forwarded to the destination.
	sc.Version = "prepare-destination"
	sc.Routes[0].ChallengeAddress = destIP
	old, err = ApplyIngress(sourceCtx, source, old, sc)
	if err != nil {
		t.Fatal(err)
	}
	if err = ProbeIngressBackend(sourceCtx, source, sc, old, "move"); err != nil {
		t.Fatal(err)
	}
	if err = ingressFile(destCtx, destination, initial.ContainerID, "fixture-root.pem", root); err != nil {
		t.Fatal(err)
	}
	dc := IngressConfig{OrgID: "org", ServerID: "destination", Version: "prepared-destination", ACMEDir: "https://" + pebbleIP + ":14000/dir", ACMERoots: []string{"/config/fixture-root.pem"}, Routes: []IngressRoute{{ID: "move", Hostname: hostname, Backend: db}}}
	applied, err := ApplyIngress(destCtx, destination, initial, dc)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(root)
	ca := ingressHTTPS(source, pebbleIP+":15000", pebbleIP, roots)
	var issuedRoot []byte
	for range 30 {
		resp, getErr := ingressGet(t, ca, "https://"+pebbleIP+":15000/roots/0")
		if getErr == nil {
			issuedRoot, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	issued := x509.NewCertPool()
	if !issued.AppendCertsFromPEM(issuedRoot) {
		t.Fatalf("issued root unavailable: %s", issuedRoot)
	}
	client := ingressHTTPS(destination, "127.0.0.1:443", hostname, issued)
	var serial string
	for range 100 {
		resp, getErr := ingressGet(t, client, "https://"+hostname+"/")
		if getErr == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if string(body) == "project-destination-release-destination\n" && resp.TLS != nil {
				serial = resp.TLS.PeerCertificates[0].SerialNumber.String()
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if serial == "" {
		t.Fatal("destination did not obtain trusted certificate through source HTTP-01 forwarding")
	}
	if err = ProbeIngressBackend(destCtx, destination, dc, applied, "move"); err != nil {
		t.Fatal(err)
	}
	if err = ProbeIngressBackend(sourceCtx, source, sc, old, "move"); err != nil {
		t.Fatal("source route interrupted", err)
	}
	// Restart the owned proxy. Caddy resumes its accepted config and stored key.
	ingressDocker(t, "exec", destHost, "docker", "restart", IngressName("org", "destination"))
	client = ingressHTTPS(destination, "127.0.0.1:443", hostname, issued)
	var resp *http.Response
	for range 30 {
		resp, err = ingressGet(t, client, "https://"+hostname+"/")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.TLS.PeerCertificates[0].SerialNumber.String() != serial {
		t.Fatal("certificate was not persisted across restart")
	}
	sc.Version = "rollback-preparation"
	sc.Routes[0].ChallengeAddress = ""
	if _, err = ApplyIngress(sourceCtx, source, old, sc); err != nil {
		t.Fatal(err)
	}
	t.Log("two pinned Caddy proxies: real HTTP-01 validation through retained source, trusted destination SNI certificate, exact destination marker, source availability, certificate persistence and forwarding rollback passed")
}

func ingressGet(t *testing.T, c *http.Client, address string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

//go:build integration

package integration

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
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The node end to end test runs on Linux, where the machine running the
// test reaches containers by their addresses, as a control plane reaches a
// node's public address. On another OS it can run inside a Linux container
// on the same Docker: LOOMPLANE_TEST_NODE_E2E_SELF names that container,
// and the test attaches it to its networks.
const (
	// e2eSelfEnv names the container this test runs in, if it does.
	e2eSelfEnv = "LOOMPLANE_TEST_NODE_E2E_SELF"
	// e2ePebbleImage is the ACME test CA the ingress suites use, pinned
	// the same way.
	e2ePebbleImage = "ghcr.io/letsencrypt/pebble:latest@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"
	// e2eDinDImage is the node's machine: Docker in Docker.
	e2eDinDImage = "docker:29-dind"
	// e2eZone is the managed test zone the fake Cloudflare serves.
	e2eZone = "node-e2e.test"
	// e2eAccount is the fake Cloudflare account that owns it.
	e2eAccount = "0123456789abcdef0123456789abcdef"
	// e2eCloudflareToken is the only token the fake accepts.
	e2eCloudflareToken = "node-e2e-cloudflare-token-0123456789"
)

// e2eNet is one Docker network the test made.
type e2eNet struct {
	Name   string
	Subnet *net.IPNet
}

// host is the address with the last byte set to last, in this network.
func (n e2eNet) host(last byte) string {
	ip := append(net.IP(nil), n.Subnet.IP.To4()...)
	ip[3] = last
	return ip.String()
}

// e2eFixture is everything the node test runs against, besides Loomplane.
type e2eFixture struct {
	t      *testing.T
	prefix string
	// ctl carries the node's connection to Loomplane, and Loomplane's to
	// the CA and the DNS fake. pubA and pubB are the node's two public
	// networks; each one's addresses count as public to Loomplane.
	ctl, pubA, pubB e2eNet
	// controlHost is where the node reaches the control plane.
	controlHost string
	self        string
	// caRoot signs the CA's and the DNS fake's own listeners.
	caRoot     []byte
	caRootFile string
	pebble     string // the CA's container
	pebbleIP   string // its address on ctl
	cloudflare string // the DNS fake's container
	cfIP       string // its address on ctl
	node       string // the node's container
}

// docker runs docker and returns its trimmed output, failing the test on
// an error.
func (f *e2eFixture) docker(args ...string) string {
	f.t.Helper()
	return docker(f.t, args...)
}

// quietDocker runs docker for cleanup and diagnostics, never failing.
func quietDocker(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)) + " (" + err.Error() + ")"
	}
	return strings.TrimSpace(string(out))
}

// network makes a labelled network, with subnet when it isn't empty.
func (f *e2eFixture) network(suffix, subnet string) e2eNet {
	f.t.Helper()
	name := f.prefix + "-" + suffix
	args := []string{"network", "create", "--label", "loomplane.test=node-e2e"}
	if subnet != "" {
		args = append(args, "--subnet", subnet)
	}
	f.docker(append(args, name)...)
	f.t.Cleanup(func() {
		for _, c := range strings.Fields(quietDocker("network", "inspect", "--format", "{{range $id, $c := .Containers}}{{$id}} {{end}}", name)) {
			quietDocker("network", "disconnect", "-f", name, c)
		}
		quietDocker("network", "rm", name)
	})
	_, cidr, err := net.ParseCIDR(f.docker("network", "inspect", "--format", "{{(index .IPAM.Config 0).Subnet}}", name))
	if err != nil {
		f.t.Fatal(err)
	}
	return e2eNet{Name: name, Subnet: cidr}
}

// address is a container's address on a network.
func (f *e2eFixture) address(container string, n e2eNet) string {
	f.t.Helper()
	return f.docker("inspect", "--format", "{{(index .NetworkSettings.Networks \""+n.Name+"\").IPAddress}}", container)
}

// container makes a container that removes itself at cleanup and logs its
// output if the test failed.
func (f *e2eFixture) container(name string, args ...string) string {
	f.t.Helper()
	f.t.Cleanup(func() {
		if f.t.Failed() {
			f.t.Logf("%s output:\n%s", name, quietDocker("logs", "--tail", "200", name))
		}
		quietDocker("rm", "-fv", name)
	})
	f.docker(append([]string{"create", "--name", name, "--label", "loomplane.test=node-e2e"}, args...)...)
	return name
}

// fixtureFile is a file a fixture container gets in /fixture.
type fixtureFile struct {
	raw  []byte
	mode os.FileMode
}

// copyFixture puts files into the container's /fixture before it starts.
func (f *e2eFixture) copyFixture(container string, files map[string]fixtureFile) {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), "fixture")
	if err := os.Mkdir(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for name, file := range files {
		if err := os.WriteFile(filepath.Join(dir, name), file.raw, file.mode); err != nil {
			f.t.Fatal(err)
		}
	}
	f.docker("cp", dir, container+":/")
}

// newE2EFixture makes the networks, attaches the test's own container to
// them when it runs in one, and works out where the node reaches the
// control plane.
func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	f := &e2eFixture{t: t, prefix: "lp-node-e2e-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10), self: os.Getenv(e2eSelfEnv)}
	// Docker gives a fixed --ip only on a network with its own subnet. This
	// one is private and outside Docker's default pools.
	f.ctl = f.network("ctl", "10.213.3.0/24")
	// TEST-NET-3 and TEST-NET-2 are global unicast and not private, so
	// Loomplane treats them as public addresses.
	f.pubA = f.network("pub-a", "203.0.113.0/24")
	f.pubB = f.network("pub-b", "198.51.100.0/24")
	if f.self != "" {
		for _, n := range []e2eNet{f.ctl, f.pubA, f.pubB} {
			f.docker("network", "connect", n.Name, f.self)
		}
		f.controlHost = f.address(f.self, f.ctl)
	} else {
		// The bridge's gateway is this machine.
		f.controlHost = f.docker("network", "inspect", "--format", "{{(index .IPAM.Config 0).Gateway}}", f.ctl.Name)
	}
	if net.ParseIP(f.controlHost) == nil {
		t.Fatalf("no control plane address on %s: %q", f.ctl.Name, f.controlHost)
	}
	return f
}

// selfSigned is a CA certificate for the IPs and names, and its key, in
// PEM. The CA's and the DNS fake's listeners serve it.
func selfSigned(t *testing.T, cn string, ips []string, names []string) (cert, key []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: names}
	for _, ip := range ips {
		tpl.IPAddresses = append(tpl.IPAddresses, net.ParseIP(ip))
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

// startCloudflare runs the fake Cloudflare on ctl: its API over plain HTTP
// on 8081, for Loomplane's Cloudflare connection, and the zone's
// nameserver on 53, for Pebble and for Loomplane's route checks.
func (f *e2eFixture) startCloudflare(arch string) {
	t := f.t
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakecloudflare")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./internal/clouds/server/testdata/fakecloudflare")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the fake Cloudflare: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	f.cfIP = f.ctl.host(11)
	cert, key := selfSigned(t, "api.cloudflare.com", nil, []string{"api.cloudflare.com"})
	f.cloudflare = f.container(f.prefix+"-cf", "--network", f.ctl.Name, "--ip", f.cfIP, "--entrypoint", "/fixture/fakecloudflare", nodesImage,
		"-token", e2eCloudflareToken, "-zone-id", "e2ezone1", "-zone", e2eZone, "-account", e2eAccount, "-ip", f.cfIP,
		"-cert", "/fixture/cf.pem", "-key", "/fixture/cf-key.pem", "-http-api", ":8081")
	f.copyFixture(f.cloudflare, map[string]fixtureFile{"fakecloudflare": {raw, 0o755}, "cf.pem": {cert, 0o644}, "cf-key.pem": {key, 0o644}})
	f.docker("start", f.cloudflare)
	waitUntil(t, 30*time.Second, "the fake Cloudflare answering", func() bool {
		resp, getErr := http.Get("http://" + f.cfIP + ":8080/status") //nolint:noctx // a fixture probe
		if getErr != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// startPebble runs the ACME test CA. Its API listens on ctl, where the
// node's proxy orders from it; it validates HTTP-01 on the public networks,
// where the node answers, and resolves names through the fake Cloudflare.
func (f *e2eFixture) startPebble() {
	t := f.t
	t.Helper()
	f.pebbleIP = f.ctl.host(10)
	cert, key := selfSigned(t, "Loomplane node end to end CA", []string{f.pebbleIP}, nil)
	f.caRoot = cert
	f.caRootFile = filepath.Join(t.TempDir(), "acme-roots.pem")
	if err := os.WriteFile(f.caRootFile, cert, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{"pebble": map[string]any{"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000",
		"certificate": "/fixture/root.pem", "privateKey": "/fixture/key.pem", "httpPort": 80, "tlsPort": 443, "retryAfter": map[string]int{"authz": 1, "order": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	f.pebble = f.container(f.prefix+"-ca", "--network", f.ctl.Name, "--ip", f.pebbleIP, "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0",
		e2ePebbleImage, "-config", "/fixture/config.json", "-dnsserver", f.cfIP+":53")
	f.copyFixture(f.pebble, map[string]fixtureFile{"root.pem": {cert, 0o644}, "key.pem": {key, 0o644}, "config.json": {config, 0o644}})
	f.docker("network", "connect", "--ip", f.pubA.host(20), f.pubA.Name, f.pebble)
	f.docker("network", "connect", "--ip", f.pubB.host(20), f.pubB.Name, f.pebble)
	f.docker("start", f.pebble)
}

// pebbleClient talks to the CA's own listeners.
func (f *e2eFixture) pebbleClient() *http.Client {
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.caRoot)
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: f.pebbleIP, MinVersion: tls.VersionTLS12}}}
}

// issuedRoot is the root Pebble signs every certificate it issues under.
// Pebble makes a new one each time it starts.
func (f *e2eFixture) issuedRoot() []byte {
	t := f.t
	t.Helper()
	var root []byte
	waitUntil(t, 60*time.Second, "Pebble's issuing root", func() bool {
		resp, err := f.pebbleClient().Get("https://" + f.pebbleIP + ":15000/roots/0") //nolint:noctx // a fixture probe
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !x509.NewCertPool().AppendCertsFromPEM(raw) {
			return false
		}
		root = raw
		return true
	})
	return root
}

// trustForThisProcess makes root, beside the system's own, the roots this
// process verifies TLS with: Loomplane checks a route's certificate with
// the system pool, which Go reads once, from SSL_CERT_FILE on Linux. It
// has to run before anything in the process verifies a certificate.
func trustForThisProcess(t *testing.T, root []byte) {
	t.Helper()
	bundle := append([]byte(nil), root...)
	for _, system := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"} {
		if raw, err := os.ReadFile(system); err == nil {
			bundle = append(append(bundle, '\n'), raw...)
			break
		}
	}
	file := filepath.Join(t.TempDir(), "roots.pem")
	if err := os.WriteFile(file, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", file)
	pool, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(root)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	// The pool was read now, with the root in it, unless something read it
	// earlier.
	if _, err = cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Fatalf("this process read its system roots before the test could add Pebble's; run the test on its own: %v", err)
	}
}

// startNode starts the node's machine, Docker in Docker on ctl and pubA,
// and waits for its Docker.
func (f *e2eFixture) startNode() {
	t := f.t
	t.Helper()
	f.node = f.container(f.prefix+"-node", "--privileged", "--network", f.ctl.Name, "--hostname", "e2e-node", "-e", "DOCKER_TLS_CERTDIR=", e2eDinDImage, "dockerd", "--host=unix:///var/run/docker.sock")
	f.docker("network", "connect", "--ip", f.pubA.host(10), f.pubA.Name, f.node)
	f.docker("start", f.node)
	waitUntil(t, 60*time.Second, "the node's Docker", func() bool {
		return exec.CommandContext(t.Context(), "docker", "exec", f.node, "docker", "info").Run() == nil
	})
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("node output:\n%s", quietDocker("exec", f.node, "tail", "-n", "300", "/var/log/loomplane-node.log"))
			t.Logf("node containers:\n%s", quietDocker("exec", f.node, "docker", "ps", "-a"))
			for _, name := range strings.Fields(quietDocker("exec", f.node, "docker", "ps", "-a", "--format", "{{.Names}}")) {
				t.Logf("%s on the node:\n%s", name, quietDocker("exec", f.node, "docker", "logs", "--tail", "60", name))
			}
		}
	})
}

// nodeHTTPS reaches hostname at address:443 the way a visitor would, and
// trusts only roots.
func nodeHTTPS(t *testing.T, address, hostname string, roots *x509.CertPool) (string, *x509.Certificate, error) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: hostname, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(address, "443"))
		},
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+hostname+"/", http.NoBody)
	if err != nil {
		return "", nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return string(body), resp.TLS.PeerCertificates[0], nil
}

// zoneResolver asks the fake Cloudflare's nameserver, as the zone's own.
func (f *e2eFixture) zoneResolver() *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(f.cfIP, "53"))
	}}
}

// helloSource is the node-hello fixture as the repository tarball the fake
// GitHub serves.
func helloSource(t *testing.T) []byte {
	t.Helper()
	root := filepath.Join("testdata", "node-hello")
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tarball(t, "acme-hello-"+exampleSHA[:7], files)
}

// mustE2E is client.must that says what the API answered when the status
// isn't the one wanted.
func mustE2E(t *testing.T, c *client, want int, method, path string, body, out any) {
	t.Helper()
	var payload io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(t.Context(), method, c.base+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", c.key)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.StatusCode, want, raw)
	}
	if out != nil && len(raw) > 0 {
		if err = json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
}

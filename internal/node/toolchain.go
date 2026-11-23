package node

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// A runner's toolchain bundle is a gzipped tar holding the Firecracker and
// jailer binaries, a guest kernel, the Loomplane guest image and the CNI
// plugins a build microVM needs, with a manifest.json that names each file
// and its sha256:
//
//	{"arch": "amd64", "files": {"firecracker": {"path": "bin/firecracker", "sha256": "..."}, ...}}
//
// The files are firecracker, jailer, kernel, rootfs, and cni/ptp,
// cni/host-local and cni/tc-redirect-tap, all in one directory. A pool pins
// the bundle's own sha256, and the runner checks it before it unpacks
// anything, then checks every file against the manifest.

// toolchainFiles are the files a bundle must hold.
var toolchainFiles = []string{"firecracker", "jailer", "kernel", "rootfs", "cni/ptp", "cni/host-local", "cni/tc-redirect-tap"}

// maxToolchainBytes bounds a bundle, packed and unpacked.
const maxToolchainBytes = 8 << 30

// DefaultToolchainRoot is where a runner unpacks toolchain bundles.
const DefaultToolchainRoot = "/opt/loomplane-runner"

type toolchainManifest struct {
	Arch  string `json:"arch"`
	Files map[string]struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// ToolchainOptions is where InstallToolchain puts what it installs.
type ToolchainOptions struct {
	URL, SHA256 string
	// Root is where bundles are unpacked; Config is the host config written
	// for the runner; StateDir and RuntimeDir are the Firecracker host's.
	Root, Config, StateDir, RuntimeDir string
	// Inventory sizes the host's limits to the machine.
	Inventory nodeproto.Inventory
	// HTTP downloads the bundle. Nil is a client that only speaks https.
	HTTP *http.Client
}

// InstallToolchain downloads the bundle at o.URL, checks it against
// o.SHA256 before unpacking anything, unpacks it under o.Root, checks every
// file against its manifest, and writes a Firecracker host config for the
// runner at o.Config unless one is there. It returns the config's path.
func InstallToolchain(ctx context.Context, o ToolchainOptions) (string, error) {
	u, err := url.Parse(o.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("--toolchain must be an https URL")
	}
	sum := strings.ToLower(strings.TrimSpace(o.SHA256))
	if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
		return "", errors.New("--toolchain-sha256 must be the bundle's sha256, 64 hex characters")
	}
	dir := filepath.Join(o.Root, "toolchain-"+sum[:16])
	if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr != nil {
		if xerr := downloadToolchain(ctx, o, u.String(), sum, dir); xerr != nil {
			return "", xerr
		}
	}
	manifest, err := checkToolchain(dir)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(o.Config); statErr == nil {
		return o.Config, nil
	}
	return o.Config, writeRunnerHostConfig(o, dir, manifest)
}

func downloadToolchain(ctx context.Context, o ToolchainOptions, address, sum, dir string) error {
	httpc := o.HTTP
	if httpc == nil {
		httpc = &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("the toolchain bundle redirected off https")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("downloading the toolchain bundle: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading the toolchain bundle: HTTP %d", resp.StatusCode)
	}
	if xerr := os.MkdirAll(o.Root, 0o755); xerr != nil { //nolint:gosec // the toolchain is world readable; only root writes it
		return xerr
	}
	tmp, err := os.CreateTemp(o.Root, ".bundle-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxToolchainBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxToolchainBytes {
		return errors.New("the toolchain bundle is too large")
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fmt.Errorf("the toolchain bundle's sha256 is %s, not the %s the pool pins; nothing was unpacked", got, sum)
	}
	staging, err := os.MkdirTemp(o.Root, ".unpack-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if xerr := unpackToolchain(tmp.Name(), staging); xerr != nil {
		return xerr
	}
	if _, err = checkToolchain(staging); err != nil {
		return err
	}
	_ = os.RemoveAll(dir)
	return os.Rename(staging, dir)
}

// unpackToolchain unpacks regular files and directories only, inside dir,
// and nothing else: no links, devices or paths that leave it.
func unpackToolchain(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("the toolchain bundle isn't gzipped: %w", err)
	}
	tr := tar.NewReader(gz)
	var total int64
	for files := 0; ; files++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if files > 10000 {
			return errors.New("the toolchain bundle holds too many files")
		}
		name := filepath.Clean(hdr.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("the toolchain bundle names %q, outside itself", hdr.Name)
		}
		target := filepath.Join(dir, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil { //nolint:gosec // see above
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxToolchainBytes {
				return errors.New("the toolchain bundle unpacks too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // see above
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755) //nolint:gosec // executables and images, world readable
			if err != nil {
				return err
			}
			_, err = io.CopyN(out, tr, hdr.Size)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("the toolchain bundle holds %q, which is not a regular file or directory", hdr.Name)
		}
	}
}

// checkToolchain reads dir's manifest and checks every file it must hold
// against its sha256, and that it is for this machine's architecture.
func checkToolchain(dir string) (*toolchainManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("the toolchain bundle has no manifest.json: %w", err)
	}
	var m toolchainManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the toolchain manifest: %w", err)
	}
	if m.Arch != runtime.GOARCH {
		return nil, fmt.Errorf("the toolchain bundle is for %s, and this machine is %s", m.Arch, runtime.GOARCH)
	}
	for _, name := range toolchainFiles {
		entry, ok := m.Files[name]
		if !ok {
			return nil, fmt.Errorf("the toolchain manifest names no %s", name)
		}
		p := filepath.Clean(entry.Path)
		if filepath.IsAbs(p) || strings.HasPrefix(p, "..") {
			return nil, fmt.Errorf("the toolchain manifest puts %s outside the bundle", name)
		}
		got, err := fileSHA256(filepath.Join(dir, p))
		if err != nil {
			return nil, fmt.Errorf("the toolchain's %s: %w", name, err)
		}
		if got != entry.SHA256 {
			return nil, fmt.Errorf("the toolchain's %s doesn't match its manifest", name)
		}
	}
	cniDir := filepath.Dir(filepath.Clean(m.Files["cni/ptp"].Path))
	for _, name := range []string{"cni/host-local", "cni/tc-redirect-tap"} {
		if filepath.Dir(filepath.Clean(m.Files[name].Path)) != cniDir {
			return nil, errors.New("the toolchain's CNI plugins must sit in one directory")
		}
	}
	return &m, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeRunnerHostConfig writes the Firecracker host config a runner builds
// with: the bundle's files, a fresh host key, and limits sized to the
// machine, each build VM the platform's size. Builds reach the internet,
// never private networks, metadata or mail ports.
func writeRunnerHostConfig(o ToolchainOptions, dir string, m *toolchainManifest) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	asset := func(name string) fc.Asset {
		return fc.Asset{Path: filepath.Join(dir, filepath.Clean(m.Files[name].Path)), SHA256: m.Files[name].SHA256}
	}
	cpus, mem, disk := o.Inventory.CPUs, o.Inventory.MemoryMiB, o.Inventory.DiskGiB
	if cpus < 2 {
		cpus = 2
	}
	usable := int(mem) - 1024 // the host keeps a GiB
	if usable < 2304 {
		usable = 2304
	}
	vms := min(max(cpus/2, 1), max(usable/(2048+128), 1))
	diskMiB := int(disk) * 1024 * 8 / 10
	if diskMiB < 16384 {
		diskMiB = 16384
	}
	cfg := fc.HostConfig{
		HostID: "runner", StateDir: o.StateDir, RuntimeDir: o.RuntimeDir, HostKey: hex.EncodeToString(key),
		Firecracker: asset("firecracker"), Jailer: asset("jailer"), Kernel: asset("kernel"), RootFS: asset("rootfs"),
		CNIBinDir:   filepath.Join(dir, filepath.Dir(filepath.Clean(m.Files["cni/ptp"].Path))),
		CNICacheDir: filepath.Join(o.StateDir, "cni-cache"),
		CNIHashes:   map[string]string{"ptp": m.Files["cni/ptp"].SHA256, "host-local": m.Files["cni/host-local"].SHA256, "tc-redirect-tap": m.Files["cni/tc-redirect-tap"].SHA256},
		Subnet:      "10.231.0.0/20", DNS: []string{"1.1.1.1", "9.9.9.9"},
		EgressCIDRs: []string{"0.0.0.0/0"}, DeniedCIDRs: []string{"169.254.0.0/16"}, DeniedPorts: []int{25, 465, 587},
		UIDBase: 100000,
		Limits:  fc.Limits{VMs: vms, VCPUs: cpus, MemoryMiB: usable, DiskMiB: diskMiB},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.Config), 0o700); err != nil {
		return err
	}
	tmp := o.Config + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, o.Config)
}

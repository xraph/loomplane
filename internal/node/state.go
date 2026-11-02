// Package node is the machine side of a Loomplane node: its identity, its
// local config, the endpoint allowlist and the handlers it serves. It runs on
// the user's machine, so it must not import the control plane.
package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	identityFile = "identity.key"
	configFile   = "node.json"
)

// Supported returns an error on a system the node does not run on.
func Supported() error { return supportedFor(runtime.GOOS) }

func supportedFor(goos string) error {
	if goos == "linux" || goos == "darwin" {
		return nil
	}
	return fmt.Errorf("loomplane node runs on Linux and macOS; this machine runs %s", goos)
}

// State is the directory a node keeps its identity and config in.
type State struct {
	Dir string

	// euid overrides os.Geteuid in tests.
	euid func() int
}

// DefaultStateDir returns the state directory for an OS. The CLI lets
// --state-dir and LOOMPLANE_NODE_STATE override it.
func DefaultStateDir(goos, home string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "loomplane-node")
	}
	return "/var/lib/loomplane-node"
}

// Config is what a node keeps in node.json.
type Config struct {
	URL                string               `json:"url"`
	NodeID             string               `json:"node_id"`
	OrgID              string               `json:"org_id"`
	IdentityGeneration int64                `json:"identity_generation"`
	Endpoints          []nodeproto.Endpoint `json:"endpoints"`
	IngressMode        string               `json:"ingress_mode"`
	DockerSocket       string               `json:"docker_socket,omitempty"`
	Edge               bool                 `json:"edge,omitempty"`
}

func (s State) uid() int {
	if s.euid != nil {
		return s.euid()
	}
	return os.Geteuid()
}

// ensureDir creates the state dir with 0700 and checks that it is a real
// directory (not a symlink), owned by this user. It tightens a looser mode.
func (s State) ensureDir() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	fi, err := os.Lstat(s.Dir)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("state dir %s is a symlink; point --state-dir at the real directory", s.Dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("state dir %s is not a directory", s.Dir)
	}
	if owner, ok := fileOwner(fi); ok && int64(owner) != int64(s.uid()) {
		return fmt.Errorf("state dir %s is owned by uid %d, not by this user (uid %d)", s.Dir, owner, s.uid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(s.Dir, 0o700); err != nil { //nolint:gosec // a directory needs the execute bit
			return fmt.Errorf("tightening %s to 0700: %w", s.Dir, err)
		}
	}
	return nil
}

// openChecked opens a state file without following symlinks and checks the
// open file: regular, owned by this user and, when private is set, with no
// group or other access.
func (s State) openChecked(path string, private bool) (*os.File, error) {
	f, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symlink; remove it", path)
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	fail := func(format string, a ...any) (*os.File, error) {
		_ = f.Close()
		return nil, fmt.Errorf(format, a...)
	}
	if !fi.Mode().IsRegular() {
		return fail("%s is not a regular file", path)
	}
	if owner, ok := fileOwner(fi); ok && int64(owner) != int64(s.uid()) {
		return fail("%s is owned by uid %d, not by this user (uid %d)", path, owner, s.uid())
	}
	if private && fi.Mode().Perm()&0o077 != 0 {
		return fail("%s is readable by other users (mode %04o); run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	return f, nil
}

// Identity returns the node's private key, creating identity.key with mode
// 0600 when it is missing. It refuses a key file that group or others can
// read, that is a symlink, or that someone else owns.
func (s State) Identity() (ed25519.PrivateKey, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir, identityFile)
	key, err := s.readKey(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}
	_, fresh, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := writeExclusive(path, fresh); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another process won the first run. Use its key.
			return s.readKey(path)
		}
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return fresh, nil
}

func (s State) readKey(path string) (ed25519.PrivateKey, error) {
	f, err := s.openChecked(path, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, ed25519.PrivateKeySize+1))
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s is not a valid identity key", path)
	}
	return ed25519.PrivateKey(b), nil
}

// LoadConfig reads node.json. A missing file returns an error wrapping
// fs.ErrNotExist. An empty ingress mode reads as "none".
func (s State) LoadConfig() (Config, error) {
	var c Config
	if err := s.ensureDir(); err != nil {
		return c, err
	}
	path := filepath.Join(s.Dir, configFile)
	f, err := s.openChecked(path, false)
	if err != nil {
		return c, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if c.IngressMode == "" {
		c.IngressMode = nodeproto.IngressNone
	}
	return c, nil
}

// SaveConfig writes node.json atomically with mode 0600.
func (s State) SaveConfig(c Config) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.Dir, configFile), b)
}

// writeTemp writes data to a 0600 temp file in path's directory and returns
// its name. The caller removes it.
func writeTemp(path string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// writeAtomic replaces path with data: temp file, fsync, rename, fsync of the
// directory. A crash never leaves a half-written file.
func writeAtomic(path string, data []byte) error {
	tmp, err := writeTemp(path, data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// writeExclusive creates path with data only if it does not exist. It links
// the finished temp file into place, so a reader never sees a partial file and
// two first runs cannot both win. It returns an error wrapping fs.ErrExist if
// the file is already there.
func writeExclusive(path string, data []byte) error {
	tmp, err := writeTemp(path, data)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Link(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

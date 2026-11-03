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
	"strconv"
	"syscall"
	"time"

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
	// PendingAllowed is what the owner allowed here that Loomplane has not
	// yet acknowledged. A Narrow cannot remove it, and every connect sends it.
	PendingAllowed []nodeproto.Endpoint `json:"pending_allowed,omitempty"`
	IngressMode    string               `json:"ingress_mode"`
	DockerSocket   string               `json:"docker_socket,omitempty"`
	Edge           bool                 `json:"edge,omitempty"`
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

// retiredKeyPrefix starts the name a retired identity key is kept under:
// identity.key.revoked-<unix seconds>.
const retiredKeyPrefix = identityFile + ".revoked-"

// RetireIdentity moves identity.key aside, to identity.key.revoked-<unix>,
// so the next Identity makes a fresh key. A join calls it when there is no
// node.json: whatever node the old key belonged to is gone or revoked, and
// a key never joins twice. The old key is kept, not deleted, for whoever
// needs to look into what it did. A missing key is not an error.
func (s State) RetireIdentity(now time.Time) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	path := filepath.Join(s.Dir, identityFile)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; remove it", path)
	}
	for i := 0; ; i++ {
		dst := filepath.Join(s.Dir, retiredKeyPrefix+strconv.FormatInt(now.Unix(), 10))
		if i > 0 {
			dst += "-" + strconv.Itoa(i)
		}
		// A link never replaces an older retired key; then the old name goes.
		lerr := os.Link(path, dst)
		if errors.Is(lerr, fs.ErrExist) {
			continue
		}
		if lerr != nil {
			if !linkUnsupported(lerr) {
				return fmt.Errorf("keeping the old key as %s: %w", dst, lerr)
			}
			if _, serr := os.Lstat(dst); serr == nil {
				continue
			}
			if rerr := os.Rename(path, dst); rerr != nil {
				return fmt.Errorf("keeping the old key as %s: %w", dst, rerr)
			}
			return syncDir(s.Dir)
		}
		if rerr := os.Remove(path); rerr != nil {
			return rerr
		}
		return syncDir(s.Dir)
	}
}

// LoadConfig reads node.json. A missing file returns an error wrapping
// fs.ErrNotExist. An empty ingress mode reads as "none".
func (s State) LoadConfig() (Config, error) {
	var c Config
	if err := s.ensureDir(); err != nil {
		return c, err
	}
	return s.readConfigFile()
}

func (s State) readConfigFile() (Config, error) {
	var c Config
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

// ReadConfig reads node.json without touching the state dir: it never
// creates it, changes its mode or changes its owner. A missing dir or file
// returns an error wrapping fs.ErrNotExist. Commands that only look, and the
// first read of those that change things, use it, so they cannot alter a
// directory that is not a node's.
func (s State) ReadConfig() (Config, error) {
	var c Config
	fi, err := os.Lstat(s.Dir)
	if err != nil {
		return c, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return c, fmt.Errorf("state dir %s is a symlink; point --state-dir at the real directory", s.Dir)
	}
	if !fi.IsDir() {
		return c, fmt.Errorf("state dir %s is not a directory", s.Dir)
	}
	if owner, ok := fileOwner(fi); ok && int64(owner) != int64(s.uid()) {
		return c, fmt.Errorf("state dir %s is owned by uid %d, not by this user (uid %d)", s.Dir, owner, s.uid())
	}
	return s.readConfigFile()
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

// DeleteConfig removes node.json and leaves the identity key. A missing file
// is not an error.
func (s State) DeleteConfig() error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.Dir, configFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(s.Dir)
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
// the file is already there. Where the file system cannot link (some FUSE and
// bind mounts), it creates the final name with O_EXCL instead: still
// exclusive, but a reader could see the file half written.
func writeExclusive(path string, data []byte) error {
	return writeExclusiveWith(path, data, os.Link)
}

func writeExclusiveWith(path string, data []byte, link func(oldname, newname string) error) error {
	tmp, err := writeTemp(path, data)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := link(tmp, path); err != nil {
		if !linkUnsupported(err) {
			return err
		}
		return createExclusive(path, data)
	}
	return syncDir(filepath.Dir(path))
}

// linkUnsupported reports whether err says the file system has no hard links.
func linkUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EPERM)
}

func createExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return syncDir(filepath.Dir(path))
}

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
	"io/fs"
	"os"
	"path/filepath"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	identityFile = "identity.key"
	configFile   = "node.json"
)

// State is the directory a node keeps its identity and config in.
type State struct{ Dir string }

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

func (s State) ensureDir() error {
	return os.MkdirAll(s.Dir, 0o700)
}

// Identity returns the node's private key, creating identity.key with mode
// 0600 when it is missing. It refuses a key file that group or others can read.
func (s State) Identity() (ed25519.PrivateKey, error) {
	path := filepath.Join(s.Dir, identityFile)
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s.createIdentity(path)
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users (mode %04o); run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s is not a valid identity key (%d bytes)", path, len(b))
	}
	return ed25519.PrivateKey(b), nil
}

func (s State) createIdentity(path string) (ed25519.PrivateKey, error) {
	if err := s.ensureDir(); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(path, key); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return key, nil
}

// LoadConfig reads node.json. A missing file returns an error wrapping
// fs.ErrNotExist. An empty ingress mode reads as "none".
func (s State) LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(s.Dir, configFile))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", filepath.Join(s.Dir, configFile), err)
	}
	if c.IngressMode == "" {
		c.IngressMode = "none"
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

// writeAtomic writes data to a 0600 temp file in the same directory, syncs it
// and renames it into place, so a crash never leaves a half-written file.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

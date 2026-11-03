package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// ceilingFile is the most a node may serve, as its owner set it on the
// machine. On a Linux service it belongs to root, mode 0640, group the
// service user, so the service can read it and never change it. node.json
// stays the service's own: the narrowed set and what is pending. The node
// serves only what is in both.
const ceilingFile = "allow.json"

// Ceiling is what allow.json holds. NodeID binds it to one join, so a copy
// kept from an earlier node can't be put back to widen a later one.
type Ceiling struct {
	NodeID    string               `json:"node_id"`
	Endpoints []nodeproto.Endpoint `json:"endpoints"`
}

// errCeilingUntrusted means allow.json exists but belongs to someone the
// reader does not take an allowlist from, or others can write it.
var errCeilingUntrusted = errors.New("allow.json is not owned by a user that may widen this node")

// ReadCeiling reads allow.json without following a symlink. trusted says
// whose file it accepts, by owner uid. A file that group or others can
// write is refused whoever owns it. A missing file returns an error wrapping
// fs.ErrNotExist. Only endpoints an owner can allow are kept.
func (s State) ReadCeiling(trusted func(uid uint32) bool) (Ceiling, error) {
	var c Ceiling
	path := filepath.Join(s.Dir, ceilingFile)
	f, err := openNoFollow(path)
	if err != nil {
		return c, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !fi.Mode().IsRegular() {
		return c, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return c, fmt.Errorf("%w: %s can be written by other users (mode %04o)", errCeilingUntrusted, path, fi.Mode().Perm())
	}
	if owner, ok := fileOwner(fi); ok && !trusted(owner) {
		return c, fmt.Errorf("%w: %s belongs to uid %d", errCeilingUntrusted, path, owner)
	}
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return Ceiling{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var keep []nodeproto.Endpoint
	for _, e := range c.Endpoints {
		if slices.Contains(nodeproto.AllowableEndpoints(), e) && !slices.Contains(keep, e) {
			keep = append(keep, e)
		}
	}
	c.Endpoints = nonNil(keep)
	return c, nil
}

// SaveCeiling writes allow.json atomically with mode 0640, owned by whoever
// runs this. A gid of 0 or more becomes its group, so a service user in that
// group can read it; -1 leaves the group as created.
func (s State) SaveCeiling(c Ceiling, gid int) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	c.Endpoints = nonNil(c.Endpoints)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, ceilingFile)
	// Group and mode are set on the descriptor CreateTemp returned, and the
	// temp file is never reopened by name. In a directory the service user
	// owns, it could swap the name for a hard link to another file between a
	// write and a reopen, and that file would get this group and mode.
	tmp, err := writeTempWith(path, b, func(f *os.File) error {
		if gid >= 0 {
			if err := f.Chown(-1, gid); err != nil {
				return fmt.Errorf("giving %s to group %d: %w", path, gid, err)
			}
		}
		return f.Chmod(0o640)
	})
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(s.Dir)
}

// deleteCeiling removes allow.json. A missing file is not an error.
func (s State) deleteCeiling() error {
	if err := os.Remove(filepath.Join(s.Dir, ceilingFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// within returns the endpoints of eps that are in the ceiling, in eps' order.
func within(eps, ceiling []nodeproto.Endpoint) []nodeproto.Endpoint {
	var out []nodeproto.Endpoint
	for _, e := range eps {
		if slices.Contains(ceiling, e) {
			out = append(out, e)
		}
	}
	return out
}

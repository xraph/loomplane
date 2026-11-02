package node

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

//go:embed templates
var templates embed.FS

const (
	systemdUnitName = "loomplane-node.service"
	serviceUser     = "loomplane-node"
	systemdUnitDir  = "/etc/systemd/system"
)

// systemdManager installs the node as a system unit that runs as its own user.
type systemdManager struct {
	run     Runner
	unitDir string
	user    string
	// lookup finds the service user's ids, and returns user.UnknownUserError
	// when there is no such user.
	lookup func(name string) (uid, gid int, err error)
	chown  func(path string, uid, gid int) error
}

func newSystemdManager(run Runner) *systemdManager {
	return &systemdManager{run: run, unitDir: systemdUnitDir, user: serviceUser, lookup: lookupUser, chown: os.Lchown}
}

func lookupUser(name string) (uid, gid int, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, err = strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err = strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// renderSystemdUnit fills the unit template. The binary it runs is the copy in
// the state dir, named like the running binary.
func renderSystemdUnit(u Unit, svcUser string) (string, error) {
	if strings.ContainsAny(u.StateDir, " \t\n\"'\\") {
		return "", fmt.Errorf("the state dir %q has a space or a quote in it, which a systemd unit cannot take; use another --state-dir", u.StateDir)
	}
	return render("templates/loomplane-node.service", map[string]string{
		"User": svcUser, "StateDir": u.StateDir, "Exec": filepath.Join(u.StateDir, "bin", filepath.Base(u.Binary)),
	})
}

func render(name string, data any) (string, error) {
	text, err := templates.ReadFile(name)
	if err != nil {
		return "", err
	}
	t, err := template.New(name).Parse(string(text))
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (m *systemdManager) Install(ctx context.Context, u Unit) error {
	unit, err := renderSystemdUnit(u, m.user)
	if err != nil {
		return err
	}
	uid, gid, err := m.ensureUser(ctx, u)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(u.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(u.StateDir, 0o700); err != nil { //nolint:gosec // a directory needs the execute bit
		return err
	}
	if err := installBinary(u.Binary, u.StateDir); err != nil {
		return err
	}
	// Task 8's checks insist the state files belong to the user that runs the
	// node, so hand over everything join made, and the copied binary.
	if err := m.chownTree(u.StateDir, uid, gid); err != nil {
		return err
	}
	if err := os.MkdirAll(m.unitDir, 0o755); err != nil { //nolint:gosec // systemd reads the unit dir as any user
		return err
	}
	if err := os.WriteFile(filepath.Join(m.unitDir, systemdUnitName), []byte(unit), 0o644); err != nil { //nolint:gosec // a unit file is world readable
		return err
	}
	return m.systemctl(ctx, "daemon-reload")
}

// ensureUser creates the service user if it is missing, and adds it to the
// docker group when Docker was found and the group exists.
func (m *systemdManager) ensureUser(ctx context.Context, u Unit) (uid, gid int, err error) {
	uid, gid, err = m.lookup(m.user)
	var unknown user.UnknownUserError
	switch {
	case err == nil:
	case errors.As(err, &unknown):
		if _, rerr := m.run.Run(ctx, "useradd", "--system", "--no-create-home", "--home-dir", u.StateDir, "--shell", "/usr/sbin/nologin", m.user); rerr != nil {
			return 0, 0, fmt.Errorf("creating the %s user: %w", m.user, rerr)
		}
		if uid, gid, err = m.lookup(m.user); err != nil {
			return 0, 0, fmt.Errorf("finding the %s user after creating it: %w", m.user, err)
		}
	default:
		return 0, 0, err
	}
	if u.Docker {
		if _, gerr := m.run.Run(ctx, "getent", "group", "docker"); gerr == nil {
			if _, err := m.run.Run(ctx, "usermod", "-aG", "docker", m.user); err != nil {
				return 0, 0, fmt.Errorf("adding %s to the docker group: %w", m.user, err)
			}
		} else if !exited(gerr) {
			return 0, 0, gerr
		}
	}
	return uid, gid, nil
}

func (m *systemdManager) chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return m.chown(p, uid, gid)
	})
}

func (m *systemdManager) systemctl(ctx context.Context, args ...string) error {
	if _, err := m.run.Run(ctx, "systemctl", args...); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (m *systemdManager) unitName() string { return strings.TrimSuffix(systemdUnitName, ".service") }

func (m *systemdManager) Start(ctx context.Context) error {
	return m.systemctl(ctx, "enable", "--now", m.unitName())
}

func (m *systemdManager) Stop(ctx context.Context) error {
	return m.systemctl(ctx, "stop", m.unitName())
}

func (m *systemdManager) Remove(ctx context.Context) error {
	// A unit that was never loaded fails to disable; the file is what matters.
	derr := m.systemctl(ctx, "disable", "--now", m.unitName())
	if err := os.Remove(filepath.Join(m.unitDir, systemdUnitName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := m.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if derr != nil && !exited(derr) {
		return derr
	}
	return nil
}

func (m *systemdManager) Running(ctx context.Context) (bool, error) {
	_, err := m.run.Run(ctx, "systemctl", "is-active", "--quiet", m.unitName())
	switch {
	case err == nil:
		return true, nil
	case exited(err):
		return false, nil
	}
	return false, err
}

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
	"regexp"
	"slices"
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
// BinDir, named like the running binary.
func renderSystemdUnit(u Unit, svcUser string) (string, error) {
	if err := checkUnit(u, true); err != nil {
		return "", err
	}
	return render("templates/loomplane-node.service", map[string]string{
		"User": svcUser, "StateDir": u.StateDir, "Exec": u.exec(),
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
	// The binary is root's, outside the state dir, so the service user
	// can't swap what systemd starts.
	if err := installBinary(u.Binary, u.binDir(), 0o755); err != nil {
		return err
	}
	// Task 8's checks insist the state files belong to the user that runs the
	// node, so hand over what join made. allow.json stays root's, and only
	// its group goes to the service, so the service can read the ceiling
	// and never raise it.
	if err := m.chownKnown(u, uid, gid); err != nil {
		return err
	}
	if err := m.chown(filepath.Join(u.StateDir, ceilingFile), -1, gid); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
// docker group when u.Docker says root allowed Docker and the group exists.
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
		if _, err := m.GrantDocker(ctx); err != nil {
			return 0, 0, err
		}
	}
	return uid, gid, nil
}

// chownKnown hands the service user the state dir and the files a node keeps
// in it, by name. It never walks the tree, so a state dir that holds anything
// else, or that points somewhere it should not, is not touched beyond itself.
func (m *systemdManager) chownKnown(u Unit, uid, gid int) error {
	for _, p := range []string{
		u.StateDir,
		filepath.Join(u.StateDir, identityFile),
		filepath.Join(u.StateDir, configFile),
	} {
		if err := m.chown(p, uid, gid); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// safePath allows only the characters a unit file can carry without quoting.
var safePath = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// checkUnit requires an absolute state dir and binary name made of safe
// characters, before either goes into a unit file or a command.
func checkUnit(u Unit, strictDir bool) error {
	if !filepath.IsAbs(u.StateDir) {
		return fmt.Errorf("the state dir %q is not an absolute path", u.StateDir)
	}
	if strictDir && !safePath.MatchString(u.StateDir) {
		return fmt.Errorf("the state dir %q has characters a systemd unit cannot take; use letters, digits and . _ - / only", u.StateDir)
	}
	if u.BinDir != "" && (!filepath.IsAbs(u.BinDir) || !safePath.MatchString(u.BinDir)) {
		return fmt.Errorf("the binary dir %q must be an absolute path of letters, digits and . _ - / only", u.BinDir)
	}
	if strings.ContainsAny(u.StateDir, "\x00\r\n") {
		return fmt.Errorf("the state dir %q has a control character in it", u.StateDir)
	}
	if name := filepath.Base(u.Binary); !safePath.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("the binary name %q has characters a service file cannot take", name)
	}
	return nil
}

func (m *systemdManager) systemctl(ctx context.Context, args ...string) error {
	if _, err := m.run.Run(ctx, "systemctl", args...); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// GrantDocker adds the service user to the docker group if there is one and
// the user is not in it already. A missing user or group is not an error:
// there is nothing to grant.
func (m *systemdManager) GrantDocker(ctx context.Context) (bool, error) {
	if _, _, err := m.lookup(m.user); err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return false, nil
		}
		return false, err
	}
	if _, err := m.run.Run(ctx, "getent", "group", "docker"); err != nil {
		if exited(err) {
			return false, nil
		}
		return false, err
	}
	out, err := m.run.Run(ctx, "id", "-nG", m.user)
	if err != nil && !exited(err) {
		return false, err
	}
	if err == nil && slices.Contains(strings.Fields(string(out)), "docker") {
		return false, nil
	}
	if _, err := m.run.Run(ctx, "usermod", "-aG", "docker", m.user); err != nil {
		return false, fmt.Errorf("adding %s to the docker group: %w", m.user, err)
	}
	return true, nil
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

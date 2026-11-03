package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const launchdLabel = "com.loomplane.node"

// launchdManager installs the node as a LaunchAgent, which runs as the person
// who joined it while they are logged in. macOS nodes are for development.
type launchdManager struct {
	run      Runner
	plistDir string
	uid      int
}

func renderLaunchdPlist(u Unit) (string, error) {
	// The macOS state dir has a space in it ("Application Support"), and the
	// plist is XML-escaped, so only the shape is checked.
	if err := checkUnit(u, false); err != nil {
		return "", err
	}
	return render("templates/com.loomplane.node.plist", map[string]string{
		"Label":    launchdLabel,
		"Exec":     xmlEscape(u.exec()),
		"StateDir": xmlEscape(u.StateDir),
		"Log":      xmlEscape(filepath.Join(u.StateDir, "node.log")),
	})
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func (m *launchdManager) plistPath() string { return filepath.Join(m.plistDir, launchdLabel+".plist") }
func (m *launchdManager) domain() string    { return "gui/" + strconv.Itoa(m.uid) }
func (m *launchdManager) target() string    { return m.domain() + "/" + launchdLabel }

func (m *launchdManager) Install(_ context.Context, u Unit) error {
	plist, err := renderLaunchdPlist(u)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(u.StateDir, 0o700); err != nil {
		return err
	}
	if err := installBinary(u.Binary, u.binDir(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(m.plistDir, 0o755); err != nil { //nolint:gosec // ~/Library/LaunchAgents is the standard mode
		return err
	}
	return os.WriteFile(m.plistPath(), []byte(plist), 0o644) //nolint:gosec // launchd wants a world readable plist
}

func (m *launchdManager) launchctl(ctx context.Context, args ...string) error {
	if _, err := m.run.Run(ctx, "launchctl", args...); err != nil {
		return fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (m *launchdManager) Start(ctx context.Context) error {
	// A copy loaded from an earlier join would keep the old arguments.
	if ok, _ := m.Running(ctx); ok { //nolint:errcheck // a failed check just tries the bootstrap
		_ = m.launchctl(ctx, "bootout", m.target()) //nolint:errcheck // reloaded below
	}
	return m.launchctl(ctx, "bootstrap", m.domain(), m.plistPath())
}

func (m *launchdManager) Stop(ctx context.Context) error {
	return m.launchctl(ctx, "bootout", m.target())
}

func (m *launchdManager) Remove(ctx context.Context) error {
	if ok, _ := m.Running(ctx); ok { //nolint:errcheck // not running, nothing to boot out
		if err := m.Stop(ctx); err != nil {
			return err
		}
	}
	if err := os.Remove(m.plistPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (m *launchdManager) Running(ctx context.Context) (bool, error) {
	_, err := m.run.Run(ctx, "launchctl", "print", m.target())
	switch {
	case err == nil:
		return true, nil
	case exited(err):
		return false, nil
	}
	return false, err
}

//go:build linux || darwin

package ctl

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sudoApp is loomctl run as root through sudo by ada, whose home is home.
// root has no login of its own at rootLogin.
func sudoApp(t *testing.T, home, sudoUID string) *App {
	t.Helper()
	env := map[string]string{"SUDO_USER": "ada", "SUDO_UID": sudoUID}
	app := New(Options{Getenv: func(k string) string { return env[k] }, ConfigPath: filepath.Join(t.TempDir(), "root.json")})
	app.opts.euid = func() int { return 0 }
	app.opts.lookupUser = func(name string) (*user.User, error) {
		return &user.User{Username: name, Uid: strconv.Itoa(os.Getuid()), HomeDir: home}, nil
	}
	return app
}

func writeLogin(t *testing.T, home string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(home, userConfigDir, "loomplane", "loomctl.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"url":"https://lp.example.com","key":"lp_live_ada"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNodeLoginUnderSudoReadsTheSudoUsersLogin(t *testing.T) {
	home := t.TempDir()
	path := writeLogin(t, home, 0o600)
	me := strconv.Itoa(os.Getuid())
	url, key, err := sudoApp(t, home, me).nodeLogin()
	if err != nil || url != "https://lp.example.com" || key != "lp_live_ada" {
		t.Fatalf("login %q %q %v", url, key, err)
	}

	// One that others can write is refused, and says why.
	if err = os.Chmod(path, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, _, err = sudoApp(t, home, me).nodeLogin(); err == nil || !strings.Contains(err.Error(), "written by other users") {
		t.Fatalf("a world-writable login: %v", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	// SUDO_UID that isn't the user's.
	if _, _, err = sudoApp(t, home, "4242").nodeLogin(); err == nil || !strings.Contains(err.Error(), "SUDO_UID") {
		t.Fatalf("a mismatched SUDO_UID: %v", err)
	}
	// A symlink is not followed.
	link := filepath.Join(t.TempDir(), "home")
	if err = os.MkdirAll(filepath.Join(link, userConfigDir, "loomplane"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(path, filepath.Join(link, userConfigDir, "loomplane", "loomctl.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = sudoApp(t, link, me).nodeLogin(); err == nil {
		t.Fatal("a symlinked login was read")
	}
}

func TestNodeLoginUnderSudoPrefersRootsOwn(t *testing.T) {
	home := t.TempDir()
	writeLogin(t, home, 0o600)
	app := sudoApp(t, home, strconv.Itoa(os.Getuid()))
	if err := app.save(Settings{URL: "https://root.example.com", Key: "lp_live_root"}); err != nil {
		t.Fatal(err)
	}
	if url, key, err := app.nodeLogin(); err != nil || url != "https://root.example.com" || key != "lp_live_root" {
		t.Fatalf("login %q %q %v", url, key, err)
	}
	// Not root, or no sudo: nothing is read.
	plain := sudoApp(t, home, strconv.Itoa(os.Getuid()))
	plain.opts.euid = func() int { return 1000 }
	if _, _, err := plain.nodeLogin(); err == nil || !strings.Contains(err.Error(), "loomctl login") {
		t.Fatalf("a non-root run read another user's login: %v", err)
	}
}

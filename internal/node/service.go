package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ErrUnsupported is what a service manager returns on a system the node does
// not install itself on.
var ErrUnsupported = errors.New("installing the node as a service is not supported on this system; use --foreground")

// systemBinDir is where a Linux service's copy of loomctl lives. It is
// root's, and the unit runs with ProtectSystem=strict, so the service user
// can't replace the binary it runs; only root's node update can.
const systemBinDir = "/usr/local/lib/loomplane-node"

// Unit describes the service to install.
type Unit struct {
	// Binary is the running binary. Install copies it into BinDir, so node
	// update can replace it without touching /usr/local/bin.
	Binary string
	// StateDir is the node's state directory.
	StateDir string
	// BinDir is where the service's copy of the binary goes: systemBinDir on
	// Linux, StateDir/bin on macOS. Empty means StateDir/bin.
	BinDir string
	// Docker is set when Docker was found, and puts the service user in the
	// docker group on Linux.
	Docker bool
}

// ServiceManager installs the node as a system service and controls it.
type ServiceManager interface {
	// Install prepares everything the service needs and registers it. It does
	// not start it.
	Install(ctx context.Context, u Unit) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// Remove stops the service and unregisters it. It leaves the state dir.
	Remove(ctx context.Context) error
	Running(ctx context.Context) (bool, error)
}

// Runner runs the system commands a service manager needs. Tests replace it,
// so none of them touches the real systemctl, useradd or launchctl.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// runError is a command that ran and exited with a status.
type runError struct {
	Code int
	Out  string
}

func (e *runError) Error() string {
	if e.Out == "" {
		return "exit status " + strconv.Itoa(e.Code)
	}
	return "exit status " + strconv.Itoa(e.Code) + ": " + e.Out
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // callers pass fixed commands
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, &runError{Code: ee.ExitCode(), Out: strings.TrimSpace(string(out))}
	}
	return out, err
}

// exited reports whether err is a command that ran and failed, and not a
// command that could not run at all.
func exited(err error) bool {
	var re *runError
	return errors.As(err, &re)
}

// defaultManager is the service manager for an OS. home is the user's home
// directory, where a macOS LaunchAgent lives.
func defaultManager(goos, home string, uid int) ServiceManager {
	switch goos {
	case "linux":
		return newSystemdManager(execRunner{})
	case "darwin":
		return &launchdManager{run: execRunner{}, plistDir: filepath.Join(home, "Library", "LaunchAgents"), uid: uid}
	}
	return unsupportedManager{}
}

type unsupportedManager struct{}

func (unsupportedManager) Install(context.Context, Unit) error { return ErrUnsupported }
func (unsupportedManager) Start(context.Context) error         { return ErrUnsupported }
func (unsupportedManager) Stop(context.Context) error          { return ErrUnsupported }
func (unsupportedManager) Remove(context.Context) error        { return ErrUnsupported }
func (unsupportedManager) Running(context.Context) (bool, error) {
	return false, ErrUnsupported
}

// binDir is where u's service binary goes.
func (u Unit) binDir() string {
	if u.BinDir != "" {
		return u.BinDir
	}
	return filepath.Join(u.StateDir, "bin")
}

// exec is the path the service runs.
func (u Unit) exec() string { return filepath.Join(u.binDir(), filepath.Base(u.Binary)) }

// installBinary copies the binary at src into binDir with mode 0755, through
// a temp file and a rename, so a running copy is never cut short. mode is
// binDir's mode when it is created. A binDir that already exists must be a
// real directory owned by whoever installs and writable by no one else, so a
// planted directory can't hand the service someone else's binary.
func installBinary(src, binDir string, mode os.FileMode) error {
	if err := os.MkdirAll(binDir, mode); err != nil {
		return err
	}
	fi, err := os.Lstat(binDir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", binDir)
	}
	if owner, ok := fileOwner(fi); ok && int64(owner) != int64(os.Geteuid()) {
		return fmt.Errorf("%s belongs to uid %d, not to the user installing the node; remove it and join again", binDir, owner)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s can be written by other users (mode %04o); remove it and join again", binDir, fi.Mode().Perm())
	}
	dst := filepath.Join(binDir, filepath.Base(src))
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("reading the running binary %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(binDir, "."+filepath.Base(src)+".tmp-*")
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// goosNow is the system this binary runs on.
func goosNow() string { return runtime.GOOS }

// stdinIsTerminal reports whether standard input is a terminal.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// systemReady checks what installing the service needs: systemd and useradd
// on Linux. Other systems need nothing from the machine here.
func systemReady(goos string) error {
	if goos != "linux" {
		return nil
	}
	if fi, err := os.Stat("/run/systemd/system"); err != nil || !fi.IsDir() {
		return errors.New("this machine does not run systemd")
	}
	if _, err := exec.LookPath("useradd"); err != nil {
		return errors.New("useradd is not installed")
	}
	return nil
}

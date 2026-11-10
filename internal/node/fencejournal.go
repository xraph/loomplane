package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/xraph/loomplane/internal/clouds/server/fencerules"
)

const (
	fenceDir       = "fence"
	fenceStateFile = "state"
	fenceLockFile  = "daemon.lock"
	// maxJournalSize is what the SSH helper reads of its journal.
	maxJournalSize = 16 << 20
)

// FenceJournal is the fence state for one Docker daemon: the epochs and the
// dirty marker the SSH helper keeps in /var/tmp/loomplane-fence-<hash>, kept
// here in the node's state directory. Lock holds daemon.lock with flock, so
// fenced requests for the daemon run one at a time, across goroutines and
// across processes. Load and Save are for the lock holder.
type FenceJournal struct {
	daemonID string
	dir      string

	mu   sync.Mutex
	lock *os.File
}

// OpenFenceJournal opens the journal for daemonID under the state directory
// dir, at fence/<sha256(daemonID)>, creating the directories 0700 and
// daemon.lock 0600. Like the SSH helper, it refuses a directory that is a
// symlink, belongs to someone else or is not exactly 0700.
func OpenFenceJournal(dir, daemonID string) (*FenceJournal, error) {
	if daemonID == "" {
		return nil, errors.New("a fence journal needs a daemon ID")
	}
	sum := sha256.Sum256([]byte(daemonID))
	root := filepath.Join(dir, fenceDir)
	j := &FenceJournal{daemonID: daemonID, dir: filepath.Join(root, hex.EncodeToString(sum[:]))}
	for _, d := range []string{root, j.dir} {
		if err := privateDir(d); err != nil {
			return nil, err
		}
	}
	f, err := j.openLock()
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return j, nil
}

// privateDir creates d with 0700 if it is missing and checks it.
func privateDir(d string) error {
	if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("creating %s: %w", d, err)
	}
	fi, err := os.Lstat(d)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink; remove it", d)
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", d)
	case fi.Mode().Perm() != 0o700:
		return fmt.Errorf("%s has mode %04o, not 0700; run: chmod 700 %s", d, fi.Mode().Perm(), d)
	}
	if owner, ok := fileOwner(fi); ok && int64(owner) != int64(os.Geteuid()) {
		return fmt.Errorf("%s is owned by uid %d, not by this user (uid %d)", d, owner, os.Geteuid())
	}
	return nil
}

// openLock opens daemon.lock, creating it 0600, and checks the open file.
func (j *FenceJournal) openLock() (*os.File, error) {
	path := filepath.Join(j.dir, fenceLockFile)
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := checkPrivateFile(path, fi); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func checkPrivateFile(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if owner, ok := fileOwner(fi); ok && int64(owner) != int64(os.Geteuid()) {
		return fmt.Errorf("%s is owned by uid %d, not by this user (uid %d)", path, owner, os.Geteuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is readable by other users (mode %04o); run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	return nil
}

// Lock takes the daemon's lock, waiting for whoever holds it.
func (j *FenceJournal) Lock() error {
	j.mu.Lock()
	f, err := j.openLock()
	if err == nil {
		if err = flock(f); err != nil {
			_ = f.Close()
		}
	}
	if err != nil {
		j.mu.Unlock()
		return fmt.Errorf("locking the fence journal: %w", err)
	}
	j.lock = f
	return nil
}

// TryLock takes the daemon's lock without waiting. It reports false, and
// holds nothing, when another request already holds it.
func (j *FenceJournal) TryLock() (bool, error) {
	if !j.mu.TryLock() {
		return false, nil
	}
	f, err := j.openLock()
	locked := false
	if err == nil {
		locked, err = tryFlock(f)
		if !locked {
			_ = f.Close()
		}
	}
	if err != nil || !locked {
		j.mu.Unlock()
		if err != nil {
			return false, fmt.Errorf("locking the fence journal: %w", err)
		}
		return false, nil
	}
	j.lock = f
	return true, nil
}

// Unlock releases the lock Lock or TryLock took.
func (j *FenceJournal) Unlock() {
	// Closing the file releases the flock too, so a failed funlock is moot.
	_ = funlock(j.lock) //nolint:errcheck // see above
	_ = j.lock.Close()
	j.lock = nil
	j.mu.Unlock()
}

// Load reads the journal. A missing or empty one is fresh. A dirty journal
// comes back with its mark: a request died mid-flight, and fencerules
// refuses to advance or admit anything until the mark clears.
func (j *FenceJournal) Load() (fencerules.Journal, error) {
	state, _, err := j.read()
	return state, err
}

// read is Load, also returning the journal's bytes as they were read, which
// are empty for a fresh journal.
func (j *FenceJournal) read() (fencerules.Journal, []byte, error) {
	fresh := fencerules.Journal{Epochs: map[string]int64{}}
	path := filepath.Join(j.dir, fenceStateFile)
	f, err := State{Dir: j.dir}.openChecked(path, true)
	if errors.Is(err, fs.ErrNotExist) {
		return fresh, nil, nil
	}
	if err != nil {
		return fencerules.Journal{}, nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxJournalSize+1))
	if err != nil {
		return fencerules.Journal{}, nil, err
	}
	if len(raw) > maxJournalSize {
		return fencerules.Journal{}, nil, fmt.Errorf("%s is over %d bytes", path, maxJournalSize)
	}
	if len(raw) == 0 {
		return fresh, raw, nil
	}
	var state fencerules.Journal
	if err := json.Unmarshal(raw, &state); err != nil {
		return fencerules.Journal{}, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if state.Epochs == nil {
		state.Epochs = map[string]int64{}
	}
	return state, raw, nil
}

// Save replaces the journal with state: a 0600 temp file, fsync, rename and
// an fsync of the directory.
func (j *FenceJournal) Save(state fencerules.Journal) error {
	if state.Epochs == nil {
		state.Epochs = map[string]int64{}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(j.dir, fenceStateFile), data)
}

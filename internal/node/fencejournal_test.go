//go:build linux || darwin

package node

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server/fencerules"
)

// journalDir is where the journal for daemon-1 lives under state.
func journalDir(state string) string {
	sum := sha256.Sum256([]byte("daemon-1"))
	return filepath.Join(state, "fence", hex.EncodeToString(sum[:]))
}

func TestJournalFilesArePrivate(t *testing.T) {
	state := t.TempDir()
	j, err := OpenFenceJournal(state, "daemon-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Lock(); err != nil {
		t.Fatal(err)
	}
	got, err := j.Load()
	if err != nil || got.Dirty.Set || len(got.Epochs) != 0 {
		t.Fatalf("fresh journal %+v %v", got, err)
	}
	got.Epochs[fencerules.JournalKey("op")] = 2
	if err = j.Save(got); err != nil {
		t.Fatal(err)
	}
	j.Unlock()

	dir := journalDir(state)
	for _, d := range []string{filepath.Join(state, "fence"), dir} {
		fi, lerr := os.Lstat(d)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v", d, fi.Mode())
		}
	}
	for _, name := range []string{"state", "daemon.lock"} {
		fi, lerr := os.Lstat(filepath.Join(dir, name))
		if lerr != nil {
			t.Fatal(lerr)
		}
		if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v", name, fi.Mode())
		}
	}

	if err = j.Lock(); err != nil {
		t.Fatal(err)
	}
	defer j.Unlock()
	again, err := j.Load()
	if err != nil || again.Epochs[fencerules.JournalKey("op")] != 2 {
		t.Fatalf("reloaded %+v %v", again, err)
	}
}

// Load hands back a dirty mark, the exec it names included, so the fence
// can settle it. Advance and Admit still refuse a dirty journal.
func TestJournalLoadKeepsTheDirtyMark(t *testing.T) {
	j, err := OpenFenceJournal(t.TempDir(), "daemon-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Lock(); err != nil {
		t.Fatal(err)
	}
	defer j.Unlock()
	mark := fencerules.ExecMark(strings.Repeat("b", 64), strings.Repeat("a", 64))
	if err = j.Save(fencerules.Journal{Epochs: map[string]int64{}, Dirty: mark}); err != nil {
		t.Fatal(err)
	}
	got, err := j.Load()
	if err != nil || got.Dirty != mark {
		t.Fatalf("loaded %+v %v, want the exec mark", got, err)
	}
	if err := fencerules.Advance(&got, "op", 1); !errors.Is(err, fencerules.ErrDirty) {
		t.Fatalf("advance %v, want ErrDirty", err)
	}
}

func TestJournalRefusesLooseDirectory(t *testing.T) {
	state := t.TempDir()
	if _, err := OpenFenceJournal(state, "daemon-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(journalDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFenceJournal(state, "daemon-1"); err == nil {
		t.Fatal("opened a journal in a 0755 directory")
	}
}

func TestJournalRefusesSymlinkedState(t *testing.T) {
	state := t.TempDir()
	j, err := OpenFenceJournal(state, "daemon-1")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte(`{"epochs":{},"dirty":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(journalDir(state), "state")); err != nil {
		t.Fatal(err)
	}
	if err := j.Lock(); err != nil {
		t.Fatal(err)
	}
	defer j.Unlock()
	if _, err := j.Load(); err == nil {
		t.Fatal("loaded a symlinked journal")
	}
}

// Two journals for one daemon, as two node processes would hold, exclude
// each other through daemon.lock.
func TestJournalLockExcludesAnotherHolder(t *testing.T) {
	state := t.TempDir()
	a, err := OpenFenceJournal(state, "daemon-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenFenceJournal(state, "daemon-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Lock(); err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		err := b.Lock()
		if err == nil {
			b.Unlock()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("second holder locked while the first held it: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	a.Unlock()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second holder never got the lock")
	}
}

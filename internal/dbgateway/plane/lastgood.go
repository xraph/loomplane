package plane

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// lastGoodContext binds the sealed file to its purpose, so a value sealed to
// the gateway's key for something else does not open as a snapshot.
var lastGoodContext = []byte("last-good")

// SaveLastGood seals s to key's public half and writes it to path, replacing
// any file there. The write goes to a temporary file in the same directory,
// mode 0600, which is synced and then renamed over path, so a crash leaves
// either the old file or the new one and never a partial one.
func SaveLastGood(path string, key *ecdh.PrivateKey, s Snapshot) (err error) {
	if key == nil {
		return errors.New("save last good: nil key")
	}
	plain, err := json.Marshal(s)
	if err != nil {
		return err
	}
	sealed, err := Seal(key.PublicKey(), plain, lastGoodContext)
	if err != nil {
		return err
	}
	data, err := json.Marshal(sealed)
	if err != nil {
		return err
	}

	// CreateTemp opens the file with mode 0600.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// LoadLastGood reads the file SaveLastGood wrote and opens it with key. A wrong
// key, or a file that was altered, returns ErrSealed. A missing file returns
// the error from os.ReadFile, which matches fs.ErrNotExist. The caller still
// verifies the snapshot before serving from it.
func LoadLastGood(path string, key *ecdh.PrivateKey) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var sealed Sealed
	if err := json.Unmarshal(data, &sealed); err != nil {
		return Snapshot{}, err
	}
	plain, err := Open(key, sealed, lastGoodContext)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(plain, &s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

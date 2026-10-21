// Package secureconfig reads bounded operator configuration without disclosing its contents.
package secureconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var ErrInvalid = errors.New("invalid operator configuration file")

// ReadJSON rejects writable shared files, symlinks, unknown fields and trailing
// values. ownerOnly also rejects group/world read access for private key files.
func ReadJSON(path string, maxBytes int64, ownerOnly bool, value any) error {
	if maxBytes <= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalid
	}
	valid := func(info os.FileInfo) bool {
		forbidden := os.FileMode(0o022)
		if ownerOnly {
			forbidden = 0o077
		}
		return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&forbidden == 0 && info.Size() > 0 && info.Size() <= maxBytes
	}
	before, err := os.Lstat(path)
	if err != nil || !valid(before) {
		return ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !valid(opened) || !os.SameFile(before, opened) {
		return ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(body)) > maxBytes {
		return ErrInvalid
	}
	defer clear(body)
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return ErrInvalid
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

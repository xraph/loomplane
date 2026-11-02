// Package enginebackup binds native backups to their recorded source and recipe.
package enginebackup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"

	"github.com/xraph/loomplane/internal/backupfiles"
)

const MaxPayload = int64(10 << 30)
const LabelEngine = "loomplane.engine.recovery"
const LabelRevision = "loomplane.engine.recipe-revision"
const LabelSource = "loomplane.engine.recovery-source"

var pinnedImage = regexp.MustCompile(`^[^@\s]+@sha256:[a-f0-9]{64}$`)

// Identity excludes credentials. A clone must match every source field before import.
type Identity struct {
	Org, Project, Environment, Resource, Instance string
	Kind, Revision, Image, Architecture, Format   string
}

func Format(engine string) string {
	switch engine {
	case "falkordb":
		return "falkordb-rdb-v1"
	case "elasticsearch":
		return "elasticsearch-snapshot-v1"
	case "opensearch":
		return "opensearch-snapshot-v1"
	case "duckdb":
		return "duckdb-checkpoint-v1"
	}
	return ""
}

func (i Identity) Validate() error {
	if i.Org == "" || i.Project == "" || i.Environment == "" || i.Resource == "" || i.Instance == "" || i.Revision == "" || i.Format == "" || Format(i.Kind) != i.Format || !pinnedImage.MatchString(i.Image) || (i.Architecture != "amd64" && i.Architecture != "arm64") {
		return errors.New("invalid native backup source")
	}
	return nil
}

// Export stages the engine's complete native output before committing an archive.
func Export(ctx context.Context, files *backupfiles.Store, key string, identity Identity, write func(context.Context, io.Writer) error) (backupfiles.Object, error) {
	if err := identity.Validate(); err != nil {
		return backupfiles.Object{}, err
	}
	payload, err := os.CreateTemp("", "loomplane-engine-payload-*")
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer os.Remove(payload.Name())
	defer payload.Close()
	limited := &boundedWriter{dst: payload, left: MaxPayload}
	if checkErr := write(ctx, limited); checkErr != nil {
		return backupfiles.Object{}, checkErr
	}
	info, err := payload.Stat()
	if err != nil {
		return backupfiles.Object{}, err
	}
	if info.Size() == 0 {
		return backupfiles.Object{}, errors.New("native backup is empty")
	}
	if _, err = payload.Seek(0, io.SeekStart); err != nil {
		return backupfiles.Object{}, err
	}
	manifest, err := json.Marshal(identity)
	if err != nil {
		return backupfiles.Object{}, err
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		archive := tar.NewWriter(writer)
		writeErr := archive.WriteHeader(&tar.Header{Name: "identity.json", Mode: 0o600, Typeflag: tar.TypeReg, Size: int64(len(manifest))})
		if writeErr == nil {
			_, writeErr = archive.Write(manifest)
		}
		if writeErr == nil {
			writeErr = archive.WriteHeader(&tar.Header{Name: "payload", Mode: 0o600, Typeflag: tar.TypeReg, Size: info.Size()})
		}
		if writeErr == nil {
			_, writeErr = io.Copy(archive, payload)
		}
		if writeErr == nil {
			writeErr = archive.Close()
		}
		writer.CloseWithError(writeErr)
		done <- writeErr
	}()
	object, err := files.Put(ctx, key, reader)
	reader.CloseWithError(err)
	return object, errors.Join(err, <-done)
}

// Verified completes checksum and identity checks before exposing native bytes.
// The caller owns the returned file and must close and remove it.
func Verified(ctx context.Context, files *backupfiles.Store, object backupfiles.Object, identity Identity) (_ *os.File, err error) {
	if checkErr := identity.Validate(); checkErr != nil {
		return nil, checkErr
	}
	if object.Size > MaxPayload+1<<20 {
		return nil, errors.New("native archive exceeds size limit")
	}
	archive, err := os.CreateTemp("", "loomplane-engine-archive-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	if checkErr := files.CopyVerified(ctx, object, archive); checkErr != nil {
		return nil, checkErr
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	tr := tar.NewReader(archive)
	header, err := tr.Next()
	if err != nil || header.Name != "identity.json" || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 16384 {
		return nil, errors.New("invalid native archive manifest")
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return nil, err
	}
	var got Identity
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&got); err != nil || got != identity {
		return nil, errors.New("native backup source or recipe does not match")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid native manifest trailing data")
	}
	header, err = tr.Next()
	if err != nil || header.Name != "payload" || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > MaxPayload {
		return nil, errors.New("invalid native archive payload")
	}
	payload, err := os.CreateTemp("", "loomplane-engine-verified-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			payload.Close()
			os.Remove(payload.Name())
		}
	}()
	if _, err = io.CopyN(payload, tr, header.Size); err != nil {
		return nil, err
	}
	if _, nextErr := tr.Next(); !errors.Is(nextErr, io.EOF) {
		return nil, errors.New("unexpected native archive entry")
	}
	// tar.Reader stops at its end marker; appended bytes are not part of this format.
	if position, seekErr := archive.Seek(0, io.SeekCurrent); seekErr != nil || position != object.Size {
		return nil, errors.New("unexpected native archive trailing data")
	}
	if _, err = payload.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return payload, nil
}

type boundedWriter struct {
	dst  io.Writer
	left int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		return 0, errors.New("native payload exceeds size limit")
	}
	n, err := w.dst.Write(p)
	w.left -= int64(n)
	return n, err
}

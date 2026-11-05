package enginebackup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
)

func archiveStore(t *testing.T) *backupfiles.Store {
	t.Helper()
	drv := localdriver.New()
	dsn := "file://" + t.TempDir()
	if err := drv.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Close(context.Background()) })
	if err = files.CreateBucket(t.Context(), "backups"); err != nil {
		t.Fatal(err)
	}
	store, err := backupfiles.New(files, "backups", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func testIdentity() Identity {
	return Identity{Org: "org", Project: "project", Environment: "env", Resource: "graph", Instance: "inst", Kind: "falkordb", Revision: "3", Image: "docker.io/falkordb/falkordb@sha256:" + strings.Repeat("a", 64), Architecture: "arm64", Format: "falkordb-rdb-v1"}
}
func TestVerifiedArchiveBindsEveryIdentityFieldAndChecksum(t *testing.T) {
	store := archiveStore(t)
	identity := testIdentity()
	object, err := Export(t.Context(), store, "org/backup", identity, func(_ context.Context, out io.Writer) error {
		_, e := io.WriteString(out, "REDIS native data")
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := Verified(t.Context(), store, object, identity)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(file)
	file.Close()
	os.Remove(file.Name())
	if err != nil || string(raw) != "REDIS native data" {
		t.Fatal("payload changed")
	}
	for _, edit := range []func(*Identity){func(i *Identity) { i.Revision = "2" }, func(i *Identity) { i.Resource = "other" }, func(i *Identity) { i.Instance = "other" }, func(i *Identity) { i.Kind = "duckdb"; i.Format = "duckdb-checkpoint-v1" }, func(i *Identity) { i.Image += "x" }, func(i *Identity) { i.Org = "other" }} {
		changed := identity
		edit(&changed)
		if f, e := Verified(t.Context(), store, object, changed); e == nil {
			f.Close()
			os.Remove(f.Name())
			t.Fatal("foreign identity accepted")
		}
	}
	object.SHA256 = strings.Repeat("0", 64)
	if _, err = Verified(t.Context(), store, object, identity); err == nil {
		t.Fatal("corrupt checksum accepted")
	}
}
func TestVerifiedArchiveRejectsExtraPathsAndLinks(t *testing.T) {
	for _, name := range []string{"../payload", "/payload", "payload", "issuer-key.pem"} {
		t.Run(name, func(t *testing.T) {
			store := archiveStore(t)
			identity := testIdentity()
			manifest, _ := json.Marshal(identity)
			var raw bytes.Buffer
			tw := tar.NewWriter(&raw)
			for _, entry := range []struct {
				name string
				data []byte
			}{{"identity.json", manifest}, {"payload", []byte("native")}, {name, []byte("extra")}} {
				if err := tw.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.data)), Typeflag: tar.TypeReg}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(entry.data); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			object, err := store.Put(t.Context(), "org/backup", &raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Verified(t.Context(), store, object, identity); err == nil {
				t.Fatal("extra archive entry accepted")
			}
		})
	}
}

func TestVerifiedArchiveRejectsLinksAndTrailingBytesAfterChecksum(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		manifestType, payloadType, extraType byte
		trailing                             []byte
		wantError                            string
	}{
		{name: "valid control"},
		{name: "symlink manifest", manifestType: tar.TypeSymlink, wantError: "invalid native archive manifest"},
		{name: "hardlink manifest", manifestType: tar.TypeLink, wantError: "invalid native archive manifest"},
		{name: "symlink payload", payloadType: tar.TypeSymlink, wantError: "invalid native archive payload"},
		{name: "hardlink payload", payloadType: tar.TypeLink, wantError: "invalid native archive payload"},
		{name: "extra symlink", extraType: tar.TypeSymlink, wantError: "unexpected native archive entry"},
		{name: "extra hardlink", extraType: tar.TypeLink, wantError: "unexpected native archive entry"},
		{name: "trailing byte", trailing: []byte("x"), wantError: "unexpected native archive trailing data"},
		{name: "trailing zero block", trailing: make([]byte, 512), wantError: "unexpected native archive trailing data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := archiveStore(t)
			temporary := t.TempDir()
			t.Setenv("TMPDIR", temporary)
			identity := testIdentity()
			manifest, err := json.Marshal(identity)
			if err != nil {
				t.Fatal(err)
			}
			var raw bytes.Buffer
			writer := tar.NewWriter(&raw)
			writeEntry := func(name string, kind byte, data []byte) {
				t.Helper()
				if kind == 0 {
					kind = tar.TypeReg
				}
				header := &tar.Header{Name: name, Mode: 0o600, Typeflag: kind, Size: int64(len(data))}
				if kind == tar.TypeSymlink || kind == tar.TypeLink {
					header.Size, header.Linkname = 0, "../../source-credentials"
					data = nil
				}
				if headerErr := writer.WriteHeader(header); headerErr != nil {
					t.Fatal(headerErr)
				}
				if _, writeErr := writer.Write(data); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			writeEntry("identity.json", tc.manifestType, manifest)
			writeEntry("payload", tc.payloadType, []byte("native control payload"))
			if tc.extraType != 0 {
				writeEntry("credentials", tc.extraType, nil)
			}
			if closeErr := writer.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			raw.Write(tc.trailing)
			encoded := bytes.Clone(raw.Bytes())
			object, err := store.Put(t.Context(), "org/semantic-envelope", bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(encoded)
			if object.Size != int64(len(encoded)) || object.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatal("stored checksum does not describe the complete test archive")
			}
			var copied bytes.Buffer
			if copyErr := store.CopyVerified(t.Context(), object, &copied); copyErr != nil || !bytes.Equal(copied.Bytes(), encoded) {
				t.Fatalf("storage integrity control: %v", copyErr)
			}
			file, err := Verified(t.Context(), store, object, identity)
			if file != nil {
				payload, readErr := io.ReadAll(file)
				closeErr := file.Close()
				removeErr := os.Remove(file.Name())
				if tc.wantError != "" {
					t.Fatal("unsafe archive exposed a payload file")
				}
				if readErr != nil || closeErr != nil || removeErr != nil || string(payload) != "native control payload" {
					t.Fatalf("valid payload or cleanup failed: %q, %v, %v, %v", payload, readErr, closeErr, removeErr)
				}
			} else if tc.wantError == "" {
				t.Fatalf("valid archive returned no payload: %v", err)
			}
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("verification error = %v, want %q", err, tc.wantError)
			}
			entries, err := os.ReadDir(temporary)
			if err != nil || len(entries) != 0 {
				t.Fatalf("verification left temporary files: %v, %v", entries, err)
			}
		})
	}
}

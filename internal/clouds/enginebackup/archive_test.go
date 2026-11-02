package enginebackup

import (
	"archive/tar"
	"bytes"
	"context"
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

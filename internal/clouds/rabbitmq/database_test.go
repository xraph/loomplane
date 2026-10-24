package rabbitmq

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
)

func TestSourceRestoreRefusedBeforeIO(t *testing.T) {
	d := Database{Run: func(context.Context, []string, io.Writer) error { t.Fatal("source reached"); return nil }}
	if d.Restore(t.Context(), nil, backupfiles.Object{}) == nil {
		t.Fatal("source restore allowed")
	}
}
func TestArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "mnesia/../../outside", "other/file", "mnesia/link"} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			w := tar.NewWriter(&b)
			h := &tar.Header{Name: name, Mode: 0600, Size: 1, Typeflag: tar.TypeReg}
			if name == "mnesia/link" {
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "/outside"
				h.Size = 0
			}
			if err := w.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				if _, err := w.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := readArchive(bytes.NewReader(b.Bytes())); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func archiveFixture(t *testing.T, values map[string]string) []byte {
	t.Helper()
	entries := map[string]string{
		"profile.json": `{"format":"rabbitmq-node-v1","node":"rabbit@localhost","version":"4.3.6"}`,
		"cluster.json": `{"disk_nodes":["rabbit@localhost"],"running_nodes":["rabbit@localhost"]}`,
		"queues.json":  `[{"name":"orders","type":"classic","durable":true,"auto_delete":false,"messages":2,"messages_persistent":2,"arguments":[]}]`,
		"vhosts.json":  `[{"name":"/"}]`, "users.json": `[{"user":"admin"},{"user":"app"}]`, "policies.json": `[]`, "parameters.json": `[]`, "mnesia/rabbit@localhost/store": "messages",
	}
	for key, value := range values {
		entries[key] = value
	}
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for name, value := range entries {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(value)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestArchiveRequiresRecoverableProfile(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"cluster", "cluster.json", `{"disk_nodes":["rabbit@localhost","rabbit@other"],"running_nodes":["rabbit@localhost"]}`},
		{"node", "profile.json", `{"format":"rabbitmq-node-v1","node":"other","version":"4.3.6"}`},
		{"ephemeral", "queues.json", `[{"name":"q","type":"classic","durable":false}]`},
		{"transient-message", "queues.json", `[{"name":"q","type":"classic","durable":true,"messages":1,"messages_persistent":0,"arguments":[]}]`},
		{"stream", "queues.json", `[{"name":"q","type":"stream","durable":true}]`},
		{"ttl", "queues.json", `[{"name":"q","type":"classic","durable":true,"arguments":[["x-message-ttl","long",100]]}]`},
		{"users", "users.json", `[{"user":"admin"},{"user":"guest"}]`},
		{"policy", "policies.json", `[{"definition":{"message-ttl":10}}]`},
		{"vhost", "vhosts.json", `[{"name":"external"}]`},
		{"shovel", "parameters.json", `[{"component":"shovel"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readArchive(bytes.NewReader(archiveFixture(t, map[string]string{tc.key: tc.value}))); err == nil {
				t.Fatal("unsupported backup accepted")
			}
		})
	}
	if m, err := readArchive(bytes.NewReader(archiveFixture(t, nil))); err != nil || len(m.Queues) != 1 || m.Queues[0].Messages != 2 {
		t.Fatal("valid message backup refused", err)
	}
}

func TestArchiveAndChecksumCheckedBeforeDestination(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		files := testFiles(t)
		data := archiveFixture(t, map[string]string{"profile.json": `{"format":"wrong"}`})
		if corrupt {
			data = archiveFixture(t, nil)
		}
		object, err := files.Put(t.Context(), "org/point", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if corrupt {
			object.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}
		d := Database{Recovery: true, Run: func(context.Context, []string, io.Writer) error {
			t.Fatal("invalid backup reached destination")
			return nil
		}, Upload: func(context.Context, string, int64, func(io.Writer) error) error {
			t.Fatal("invalid backup uploaded")
			return nil
		}}
		if d.Restore(t.Context(), files, object) == nil {
			t.Fatal("invalid backup accepted")
		}
	}
}

func TestQuorumMessagesAreIntrinsicallyPersistent(t *testing.T) {
	data := archiveFixture(t, map[string]string{"queues.json": `[{"name":"orders","type":"quorum","durable":true,"messages":2,"messages_persistent":"","arguments":[["x-queue-type","longstr","quorum"]]}]`})
	m, err := readArchive(bytes.NewReader(data))
	if err != nil || len(m.Queues) != 1 || m.Queues[0].Persistent != 2 {
		t.Fatal("quorum persistence depends on a classic-only statistic", err)
	}
}

func TestMissingOrExpiredMessagesCannotVerifyRecovery(t *testing.T) {
	files := testFiles(t)
	object, err := files.Put(t.Context(), "org/expiry", bytes.NewReader(archiveFixture(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	d := Database{Recovery: true, Run: func(_ context.Context, args []string, out io.Writer) error {
		if strings.Contains(args[2], "list_queues") {
			queries++
			value := "[]"
			if queries == 2 {
				value = `[{"name":"orders","type":"classic","durable":true,"messages":1,"messages_persistent":1,"arguments":[]}]`
			}
			_, e := io.WriteString(out, value)
			return e
		}
		if strings.Contains(args[2], "authenticate_user") && !strings.Contains(args[2], "stop_app") {
			t.Fatal("missing messages reached final verification")
		}
		return nil
	}, Upload: func(_ context.Context, _ string, _ int64, write func(io.Writer) error) error {
		return write(io.Discard)
	}}
	if err = d.Restore(t.Context(), files, object); err == nil || !strings.Contains(err.Error(), "expired messages cannot be verified") || queries != 2 {
		t.Fatal("missing or expired messages marked recovered", err)
	}
}

func testFiles(t *testing.T) *backupfiles.Store {
	t.Helper()
	dsn := "file://" + t.TempDir()
	drv := localdriver.New()
	if err := drv.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close(context.Background()) })
	if err = files.CreateBucket(t.Context(), "backups"); err != nil {
		t.Fatal(err)
	}
	store, err := backupfiles.New(files, "backups", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

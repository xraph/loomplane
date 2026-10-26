package postgres

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
)

func TestExportPropagatesNativeFailureWithoutPublishing(t *testing.T) {
	driver := localdriver.New()
	backupDSN := "file://" + t.TempDir()
	if err := driver.Open(t.Context(), backupDSN); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(driver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := files.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if err = files.CreateBucket(t.Context(), "backup"); err != nil {
		t.Fatal(err)
	}
	store, err := backupfiles.New(files, "backup", backupDSN)
	if err != nil {
		t.Fatal(err)
	}
	db := Database{Name: "database", Run: func(_ context.Context, args []string, out io.Writer) error {
		if args[0] != "pg_dump" {
			t.Fatalf("unexpected tool: %v", args)
		}
		if _, writeErr := io.WriteString(out, "partial"); writeErr != nil {
			return writeErr
		}
		return errors.New("native dump failed")
	}}
	if _, err = db.Export(t.Context(), store, "failed.dump"); err == nil {
		t.Fatal("published a failed native export")
	}
	if _, err = files.Head(t.Context(), "backup", "failed.dump"); err == nil {
		t.Fatal("failed recovery point remains readable")
	}
}

func TestRestoreRequiresRecoveryBeforeReadingFiles(t *testing.T) {
	db := Database{Name: "database", Run: func(context.Context, []string, io.Writer) error { t.Fatal("restored over the live source"); return nil }}
	if err := db.Restore(t.Context(), nil, backupfiles.Object{}); err == nil {
		t.Fatal("source restore accepted")
	}
}

func TestConfigureDoesNotExposePasswordInArgumentsAndRemovesFiles(t *testing.T) {
	password := "a-long-fixture-password:with\\characters"
	uploaded := map[string][]byte{}
	removed := map[string]bool{}
	db := Database{Name: "database", Upload: func(_ context.Context, name string, _ int64, write func(io.Writer) error) error {
		var b bytes.Buffer
		if err := write(&b); err != nil {
			return err
		}
		uploaded[name] = b.Bytes()
		return nil
	}, Run: func(_ context.Context, args []string, out io.Writer) error {
		for _, arg := range args {
			if strings.Contains(arg, password) {
				t.Fatal("password entered command arguments")
			}
		}
		if args[0] == "rm" {
			for _, path := range args[2:] {
				removed[strings.TrimPrefix(path, "/tmp/")] = true
			}
			return nil
		}
		if args[0] == "env" {
			_, err := io.WriteString(out, "app\n")
			return err
		}
		if args[0] != "psql" {
			t.Fatalf("unexpected command %v", args)
		}
		return nil
	}}
	if err := db.Configure(t.Context(), password); err != nil {
		t.Fatal(err)
	}
	if len(uploaded) != 2 || len(removed) != 2 {
		t.Fatalf("temporary credential files were not removed: %d %d", len(uploaded), len(removed))
	}
	for name := range uploaded {
		if !removed[name] {
			t.Fatalf("retained credential file %s", name)
		}
	}
}

type restoreCall struct {
	args     []string
	uploaded string
}

// restoreHarness records every Run and Upload a restore makes, in order. The
// dump is the fixed backup object; list is what `pg_restore -l` prints.
type restoreHarness struct {
	t       *testing.T
	calls   []restoreCall
	list    string
	roles   string // what SELECT rolname FROM pg_roles prints
	failOn  string
	db      Database
	store   *backupfiles.Store
	object  backupfiles.Object
	uploads map[string]string
}

func newRestoreHarness(t *testing.T, extensions []string, list string) *restoreHarness {
	t.Helper()
	driver := localdriver.New()
	dsn := "file://" + t.TempDir()
	if err := driver.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(driver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := files.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if err = files.CreateBucket(t.Context(), "backup"); err != nil {
		t.Fatal(err)
	}
	store, err := backupfiles.New(files, "backup", dsn)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Put(t.Context(), "restore.dump", strings.NewReader("dump-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	h := &restoreHarness{t: t, list: list, roles: "postgres\napp\npg_database_owner\n", store: store, object: object, uploads: map[string]string{}}
	h.db = Database{Name: "db", Recovery: true, Extensions: extensions,
		Upload: func(_ context.Context, name string, _ int64, write func(io.Writer) error) error {
			var b bytes.Buffer
			if writeErr := write(&b); writeErr != nil {
				return writeErr
			}
			h.uploads[name] = b.String()
			h.calls = append(h.calls, restoreCall{uploaded: name})
			return nil
		},
		Run: func(_ context.Context, args []string, out io.Writer) error {
			h.calls = append(h.calls, restoreCall{args: args})
			joined := strings.Join(args, " ")
			if len(args) > 1 && args[0] == "pg_restore" && args[1] == "-l" {
				_, err := io.WriteString(out, h.list)
				return err
			}
			if strings.Contains(joined, "SELECT rolname FROM pg_roles") {
				_, err := io.WriteString(out, h.roles)
				return err
			}
			if h.failOn != "" && strings.Contains(joined, h.failOn) {
				return errors.New("failed: " + h.failOn)
			}
			return nil
		}}
	return h
}

// commands renders each call on one line, with temporary file names replaced
// by DUMP and LIST so the sequence is comparable.
func (h *restoreHarness) commands() []string {
	var dump, list string
	for _, c := range h.calls {
		if c.uploaded == "" {
			continue
		}
		if strings.HasSuffix(c.uploaded, ".dump") {
			dump = c.uploaded
		} else {
			list = c.uploaded
		}
	}
	out := make([]string, 0, len(h.calls))
	for _, c := range h.calls {
		var line string
		if c.uploaded != "" {
			line = "upload " + c.uploaded
		} else {
			line = strings.Join(c.args, " ")
		}
		if dump != "" {
			line = strings.ReplaceAll(line, dump, "DUMP")
		}
		if list != "" {
			line = strings.ReplaceAll(line, list, "LIST")
		}
		out = append(out, line)
	}
	return out
}

func TestRestoreWithoutExtensionsUnchanged(t *testing.T) {
	h := newRestoreHarness(t, nil, "")
	if err := h.db.Restore(t.Context(), h.store, h.object); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"upload DUMP",
		"pg_restore -U postgres -d db --single-transaction --exit-on-error --no-owner --no-acl --role=app /tmp/DUMP",
		"rm -f /tmp/DUMP",
	}
	if got := h.commands(); !slices.Equal(got, want) {
		t.Fatalf("restore without extensions changed:\n got %q\nwant %q", got, want)
	}
}

const extensionTOC = `;
; Archive created at 2026-10-21 10:00:00 UTC
;     dbname: db
; Selected TOC Entries:
;
2; 3079 16400 EXTENSION - vector` + " " + `
3; 0 0 COMMENT - EXTENSION vector` + " " + `
4; 1259 16500 TABLE public items app
5; 0 16500 TABLE DATA public items app
6; 0 0 COMMENT public TABLE items app
`

func TestRestoreWithExtensionsCreatesThemFirstAndFiltersTOC(t *testing.T) {
	h := newRestoreHarness(t, []string{"timescaledb", "vector", "postgis"}, extensionTOC)
	if err := h.db.Restore(t.Context(), h.store, h.object); err != nil {
		t.Fatal(err)
	}
	psql := "psql -U postgres -d db -v ON_ERROR_STOP=1 -c "
	want := []string{
		"upload DUMP",
		"pg_restore -l /tmp/DUMP",
		psql + "CREATE EXTENSION IF NOT EXISTS vector",
		psql + "CREATE EXTENSION IF NOT EXISTS postgis",
		psql + "CREATE EXTENSION IF NOT EXISTS timescaledb",
		"psql -U postgres -d db -At -v ON_ERROR_STOP=1 -c SELECT rolname FROM pg_roles",
		"upload LIST",
		psql + "SELECT timescaledb_pre_restore()",
		"pg_restore -U postgres -d db --single-transaction --exit-on-error --no-acl -L /tmp/LIST /tmp/DUMP",
		psql + "SELECT timescaledb_post_restore()",
		"rm -f /tmp/DUMP /tmp/LIST",
	}
	if got := h.commands(); !slices.Equal(got, want) {
		t.Fatalf("wrong restore sequence:\n got %q\nwant %q", got, want)
	}
	var list string
	for name, body := range h.uploads {
		if !strings.HasSuffix(name, ".dump") {
			list = body
		}
	}
	if strings.Contains(list, "EXTENSION") {
		t.Fatalf("list still restores or comments on an extension:\n%s", list)
	}
	for _, keep := range []string{"4; 1259 16500 TABLE public items app", "5; 0 16500 TABLE DATA public items app", "6; 0 0 COMMENT public TABLE items app", "; Selected TOC Entries:"} {
		if !strings.Contains(list, keep) {
			t.Fatalf("list lost %q:\n%s", keep, list)
		}
	}
}

func TestRestoreSkipsTimescaleHooksWithoutTimescale(t *testing.T) {
	h := newRestoreHarness(t, []string{"vector"}, extensionTOC)
	if err := h.db.Restore(t.Context(), h.store, h.object); err != nil {
		t.Fatal(err)
	}
	for _, line := range h.commands() {
		if strings.Contains(line, "timescaledb_") {
			t.Fatalf("ran a timescaledb hook without the extension: %s", line)
		}
	}
}

func TestRestoreRunsPostRestoreAfterAFailure(t *testing.T) {
	h := newRestoreHarness(t, []string{"timescaledb"}, extensionTOC)
	h.failOn = "--exit-on-error"
	err := h.db.Restore(t.Context(), h.store, h.object)
	if err == nil || !strings.Contains(err.Error(), "failed: --exit-on-error") {
		t.Fatalf("restore error lost: %v", err)
	}
	got := h.commands()
	if n := len(got); n < 3 || !strings.HasSuffix(got[n-2], "SELECT timescaledb_post_restore()") || got[n-1] != "rm -f /tmp/DUMP /tmp/LIST" {
		t.Fatalf("post restore or cleanup skipped after a failure: %q", got)
	}
}

func TestRestoreReturnsBothErrorsWhenRestoreAndPostRestoreFail(t *testing.T) {
	h := newRestoreHarness(t, []string{"timescaledb"}, extensionTOC)
	inner := h.db.Run
	h.db.Run = func(ctx context.Context, args []string, out io.Writer) error {
		joined := strings.Join(args, " ")
		if err := inner(ctx, args, out); err != nil {
			return err
		}
		switch {
		case strings.Contains(joined, "--exit-on-error"):
			return errors.New("failed: restore")
		case strings.Contains(joined, "timescaledb_post_restore"):
			return errors.New("failed: post restore")
		}
		return nil
	}
	err := h.db.Restore(t.Context(), h.store, h.object)
	if err == nil || !strings.Contains(err.Error(), "failed: restore") || !strings.Contains(err.Error(), "failed: post restore") {
		t.Fatalf("expected both errors: %v", err)
	}
}

func TestRestoreRejectsUnsafeExtensionNames(t *testing.T) {
	for _, name := range []string{"vector; drop", "Vector", "", "1vector", "vec-tor", "a" + strings.Repeat("b", 63)} {
		t.Run(name, func(t *testing.T) {
			db := Database{Name: "db", Recovery: true, Extensions: []string{"timescaledb", name},
				Run: func(context.Context, []string, io.Writer) error {
					t.Fatal("ran before validating extensions")
					return nil
				},
				Upload: func(context.Context, string, int64, func(io.Writer) error) error {
					t.Fatal("uploaded before validating extensions")
					return nil
				}}
			if err := db.Restore(t.Context(), nil, backupfiles.Object{}); err == nil {
				t.Fatalf("accepted %q", name)
			}
		})
	}
}

func TestParseExtensions(t *testing.T) {
	if got := ParseExtensions(""); got != nil {
		t.Fatalf("empty label: %v", got)
	}
	if got := ParseExtensions("timescaledb,vector,postgis"); !slices.Equal(got, []string{"timescaledb", "vector", "postgis"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRestoreKeepsExtensionEntriesItDidNotCreate(t *testing.T) {
	toc := `2; 3079 16400 EXTENSION - vector` + " " + `
3; 0 0 COMMENT - EXTENSION vector` + " " + `
4; 3079 16401 EXTENSION - pgcrypto` + " " + `
5; 0 0 COMMENT - EXTENSION pgcrypto` + " " + `
6; 1259 16500 TABLE public items app
`
	h := newRestoreHarness(t, []string{"vector"}, toc)
	if err := h.db.Restore(t.Context(), h.store, h.object); err != nil {
		t.Fatal(err)
	}
	var list string
	for name, body := range h.uploads {
		if !strings.HasSuffix(name, ".dump") {
			list = body
		}
	}
	for _, keep := range []string{"EXTENSION - pgcrypto", "COMMENT - EXTENSION pgcrypto", "TABLE public items app"} {
		if !strings.Contains(list, keep) {
			t.Fatalf("list lost %q:\n%s", keep, list)
		}
	}
	for _, drop := range []string{"EXTENSION - vector", "COMMENT - EXTENSION vector"} {
		if strings.Contains(list, drop) {
			t.Fatalf("list kept %q:\n%s", drop, list)
		}
	}
}

func TestRestoreRunsAsPostgresWithOriginalOwners(t *testing.T) {
	h := newRestoreHarness(t, []string{"timescaledb"}, extensionTOC)
	if err := h.db.Restore(t.Context(), h.store, h.object); err != nil {
		t.Fatal(err)
	}
	for _, line := range h.commands() {
		if !strings.HasPrefix(line, "pg_restore -U postgres") || strings.Contains(line, "-l ") {
			continue
		}
		for _, banned := range []string{"--no-owner", "--role=app", "--use-set-session-authorization"} {
			if strings.Contains(line, banned) {
				t.Fatalf("restore still uses %s: %s", banned, line)
			}
		}
	}
}

const ownersTOC = `;
; Selected TOC Entries:
;
2; 3079 16400 EXTENSION - vector` + " " + `
3; 0 0 COMMENT - EXTENSION vector` + " " + `
4; 2615 2200 SCHEMA - public pg_database_owner
5; 1259 16500 TABLE public items app
6; 1259 16501 TABLE public readings tsdbadmin
7; 0 16501 TABLE DATA _timescaledb_catalog hypertable postgres
8; 2606 16502 CONSTRAINT public items items_pkey fabriq_ro
9; 1259 16503 SEQUENCE SET public items_id_seq tsdbadmin
`

func TestRestoreCreatesOwnersTheDatabaseLacks(t *testing.T) {
	h := newRestoreHarness(t, []string{"vector"}, ownersTOC)
	if err := h.db.Restore(t.Context(), h.store, h.object); err != nil {
		t.Fatal(err)
	}
	psql := "psql -U postgres -d db -v ON_ERROR_STOP=1 -c "
	role := ` NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION`
	var creates []string
	for _, line := range h.commands() {
		if strings.Contains(line, "CREATE ROLE") {
			creates = append(creates, line)
		}
	}
	// postgres, app and pg_database_owner exist; the others are created once, sorted.
	want := []string{psql + `CREATE ROLE "fabriq_ro"` + role, psql + `CREATE ROLE "tsdbadmin"` + role}
	if !slices.Equal(creates, want) {
		t.Fatalf("created roles:\n got %q\nwant %q", creates, want)
	}
	// The roles exist before the restore needs them.
	got := h.commands()
	restore := slices.IndexFunc(got, func(l string) bool {
		return strings.HasPrefix(l, "pg_restore -U postgres") && !strings.Contains(l, " -l ")
	})
	if last := slices.IndexFunc(got, func(l string) bool { return strings.Contains(l, `CREATE ROLE "tsdbadmin"`) }); last < 0 || restore < last {
		t.Fatalf("roles are created after the restore: %q", got)
	}
}

func TestRestoreCreatesNoOwnerTheDatabaseHas(t *testing.T) {
	h := newRestoreHarness(t, []string{"vector"}, extensionTOC)
	if err := h.db.Restore(t.Context(), h.store, h.object); err != nil {
		t.Fatal(err)
	}
	for _, line := range h.commands() {
		if strings.Contains(line, "CREATE ROLE") {
			t.Fatalf("created a role the database has: %s", line)
		}
	}
}

func TestRestoreRefusesAnUnsafeOwnerBeforeChangingAnything(t *testing.T) {
	for _, owner := range []string{`Evil`, `a";drop`, `a"; DROP ROLE postgres; --`, `1app`, `app-user`} {
		t.Run(owner, func(t *testing.T) {
			h := newRestoreHarness(t, []string{"timescaledb", "vector"}, "5; 1259 16500 TABLE public items "+owner+"\n")
			err := h.db.Restore(t.Context(), h.store, h.object)
			if err == nil || !strings.Contains(err.Error(), "owned by") {
				t.Fatalf("accepted owner %q: %v", owner, err)
			}
			for _, line := range h.commands() {
				if strings.HasPrefix(line, "psql") || strings.HasPrefix(line, "pg_restore -U") || strings.HasPrefix(line, "upload LIST") {
					t.Fatalf("changed the database before refusing: %s", line)
				}
			}
		})
	}
}

func TestTOCOwnersIgnoresEntriesWithoutOne(t *testing.T) {
	got, err := tocOwners(ownersTOC)
	if err != nil {
		t.Fatal(err)
	}
	// The extension entries end in a space after their tag and have no owner.
	if want := []string{"app", "fabriq_ro", "pg_database_owner", "postgres", "tsdbadmin"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTOCOwnersReadsTableAndColumnInDefaultEntries(t *testing.T) {
	got, err := tocOwners("4012; 2604 19804 DEFAULT public fabriq_document_revision id postgres\n")
	if err != nil || !slices.Equal(got, []string{"postgres"}) {
		t.Fatalf("DEFAULT owners = %q, error %v", got, err)
	}
}

func TestTOCOwnersRefuseAnOwnerSplitIntoSeveralFields(t *testing.T) {
	for _, line := range []string{
		"5; 1259 16500 TABLE public items my role",
		"5; 0 16500 TABLE DATA public items my role",
		"6; 2606 16502 CONSTRAINT public items items_pkey my role",
		"7; 0 0 SEQUENCE SET public items_id_seq app owner",
	} {
		if owners, err := tocOwners(line + "\n"); err == nil || !strings.Contains(err.Error(), "owned by") {
			t.Errorf("%q: owners %q, error %v", line, owners, err)
		}
	}
	// An entry cut short of its owner is refused rather than guessed at.
	if _, err := tocOwners("5; 1259 16500 TABLE public\n"); err == nil {
		t.Error("accepted a TABLE entry with no tag and owner")
	}
}

func TestRestoreRefusesASplitOwnerBeforeChangingAnything(t *testing.T) {
	h := newRestoreHarness(t, []string{"timescaledb"}, "5; 1259 16500 TABLE public items my role\n")
	if err := h.db.Restore(t.Context(), h.store, h.object); err == nil {
		t.Fatal("accepted an owner with a space")
	}
	for _, line := range h.commands() {
		if strings.HasPrefix(line, "psql") || strings.HasPrefix(line, "pg_restore -U") {
			t.Fatalf("changed the database before refusing: %s", line)
		}
	}
}

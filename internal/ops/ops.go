// Package ops is what running Loomplane takes besides running it: backing
// up its database, restoring one, and upgrading safely.
//
// A backup is one file: a gzipped tar of every table in Loomplane's
// database, Loomplane's own and every library's, copied in one consistent
// snapshot, with a manifest saying which Loomplane made it and which
// migrations its schema had. Restoring brings an empty database to that
// schema first, then loads the rows, in one transaction.
//
// Secrets in a backup stay as they are in the database: Vault's ciphertext
// and API keys' hashes. Restoring them needs the same secret_key, so keep
// that key somewhere other than beside the backups.
package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"
)

// Format names the backup format. Restore refuses any other.
const Format = "loomplane.backup/v1"

// Manifest describes a backup.
type Manifest struct {
	Format    string    `json:"format"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	// Migrations are the schema the rows fit: group:version.
	Migrations []string `json:"migrations"`
	Tables     []Table  `json:"tables"`
}

// Table is one table in a backup.
type Table struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

const manifestFile = "manifest.json"

// skipped are tables a backup leaves out: the migration bookkeeping, which
// restoring rebuilds by migrating.
var skipped = map[string]bool{
	"public." + migrate.MigrationTable:     true,
	"public." + migrate.MigrationLockTable: true,
}

func connect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	return conn, nil
}

// tables lists the database's tables, in an order that loads parents
// before the rows that reference them.
func tables(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT n.nspname || '.' || c.relname
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `
		SELECT cn.nspname || '.' || c.relname, pn.nspname || '.' || p.relname
		FROM pg_constraint k
		JOIN pg_class c ON c.oid = k.conrelid JOIN pg_namespace cn ON cn.oid = c.relnamespace
		JOIN pg_class p ON p.oid = k.confrelid JOIN pg_namespace pn ON pn.oid = p.relnamespace
		WHERE k.contype = 'f'`)
	if err != nil {
		return nil, err
	}
	parents := map[string][]string{}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, err
		}
		if child != parent {
			parents[child] = append(parents[child], parent)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]int{}
	var visit func(string)
	visit = func(t string) {
		if seen[t] != 0 {
			return
		}
		seen[t] = 1
		for _, p := range parents[t] {
			visit(p)
		}
		seen[t] = 2
		out = append(out, t)
	}
	for _, n := range names {
		if !skipped[n] {
			visit(n)
		}
	}
	return out, nil
}

func quoted(table string) string {
	schema, name, _ := strings.Cut(table, ".")
	return pgx.Identifier{schema, name}.Sanitize()
}

// Backup writes a backup of the database to w.
func Backup(ctx context.Context, dsn, version string, w io.Writer) (*Manifest, error) {
	conn, err := connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	// One snapshot, so the tables agree with each other however long the
	// copy takes, and nothing written meanwhile is half in it.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // read-only

	m := &Manifest{Format: Format, Version: version, CreatedAt: time.Now().UTC()}
	applied, err := appliedIn(ctx, tx)
	if err != nil {
		return nil, err
	}
	m.Migrations = applied
	names, err := tables(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("list the tables: %w", err)
	}

	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	for _, t := range names {
		// Each table is buffered whole, since tar needs its size first. A
		// spill file would lift that; Loomplane's tables are small.
		var buf strings.Builder
		tag, cerr := tx.Conn().PgConn().CopyTo(ctx, &buf, "COPY "+quoted(t)+" TO STDOUT")
		if cerr != nil {
			return nil, fmt.Errorf("copy %s: %w", t, cerr)
		}
		m.Tables = append(m.Tables, Table{Name: t, Rows: tag.RowsAffected()})
		if werr := writeFile(tw, "tables/"+t+".copy", []byte(buf.String())); werr != nil {
			return nil, werr
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFile(tw, manifestFile, data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return m, zw.Close()
}

func writeFile(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// appliedIn reads the applied migrations, as group:version, sorted.
func appliedIn(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT "group" || ':' || version FROM `+migrate.MigrationTable+` ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("read the applied migrations; is this a Loomplane database? %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ErrNotEmpty is a restore into a database a Loomplane has already run on.
var ErrNotEmpty = errors.New("a Loomplane has already run on this database; restore into an empty one, or pass --force to replace what it has")

// RestoreOptions control a restore.
type RestoreOptions struct {
	// Force replaces whatever the database has. Without it, only a
	// database no Loomplane has migrated is restored into.
	Force bool
}

// migrated reports whether any Loomplane has migrated the database.
func migrated(ctx context.Context, dsn string) (bool, error) {
	conn, err := connect(ctx, dsn)
	if err != nil {
		return false, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	var exists bool
	if qerr := conn.QueryRow(ctx, `SELECT to_regclass('public.`+migrate.MigrationTable+`') IS NOT NULL`).Scan(&exists); qerr != nil || !exists {
		return false, qerr
	}
	var rows bool
	err = conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+migrate.MigrationTable+`)`).Scan(&rows)
	return rows, err
}

// Restore loads a backup into the database: it migrates it to the
// backup's schema with groups, which must be this Loomplane's, then
// replaces every table's rows, in one transaction.
func Restore(ctx context.Context, dsn string, groups []*migrate.Group, r io.Reader, opts RestoreOptions) (*Manifest, error) {
	m, files, err := read(r)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, g := range groups {
		for _, mg := range g.Migrations() {
			known[mg.Group+":"+mg.Version] = true
		}
	}
	for _, mg := range m.Migrations {
		if !known[mg] {
			return nil, fmt.Errorf("the backup's schema has migration %s, which this Loomplane doesn't; restore it with Loomplane %s or later", mg, m.Version)
		}
	}
	if !opts.Force {
		used, uerr := migrated(ctx, dsn)
		if uerr != nil {
			return nil, uerr
		}
		if used {
			return nil, ErrNotEmpty
		}
	}
	if merr := Migrate(ctx, dsn, groups); merr != nil {
		return nil, merr
	}

	conn, err := connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op once committed
	names, err := tables(ctx, tx)
	if err != nil {
		return nil, err
	}
	quotedAll := make([]string, 0, len(names))
	for _, t := range names {
		quotedAll = append(quotedAll, quoted(t))
	}
	if len(quotedAll) > 0 {
		if _, err := tx.Exec(ctx, "TRUNCATE "+strings.Join(quotedAll, ", ")+" CASCADE"); err != nil {
			return nil, fmt.Errorf("empty the tables: %w", err)
		}
	}
	for _, t := range names {
		data, ok := files["tables/"+t+".copy"]
		if !ok {
			continue // a table this Loomplane has and the backup's didn't
		}
		if _, err := tx.Conn().PgConn().CopyFrom(ctx, strings.NewReader(string(data)), "COPY "+quoted(t)+" FROM STDIN"); err != nil {
			return nil, fmt.Errorf("load %s: %w", t, err)
		}
	}
	if err := resetSequences(ctx, tx); err != nil {
		return nil, err
	}
	return m, tx.Commit(ctx)
}

// resetSequences moves each sequence past the highest value its column
// has, so new rows don't collide with restored ones.
func resetSequences(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `
		SELECT n.nspname || '.' || c.relname, a.attname, pg_get_serial_sequence(quote_ident(n.nspname) || '.' || quote_ident(c.relname), a.attname)
		FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND pg_get_serial_sequence(quote_ident(n.nspname) || '.' || quote_ident(c.relname), a.attname) IS NOT NULL`)
	if err != nil {
		return err
	}
	type seq struct{ table, column, sequence string }
	var seqs []seq
	for rows.Next() {
		var s seq
		if err := rows.Scan(&s.table, &s.column, &s.sequence); err != nil {
			return err
		}
		seqs = append(seqs, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range seqs {
		_, err := tx.Exec(ctx, fmt.Sprintf(`SELECT setval($1, COALESCE((SELECT max(%s) FROM %s), 0) + 1, false)`,
			pgx.Identifier{s.column}.Sanitize(), quoted(s.table)), s.sequence)
		if err != nil {
			return fmt.Errorf("reset %s: %w", s.sequence, err)
		}
	}
	return nil
}

// read unpacks a backup.
func read(r io.Reader) (*Manifest, map[string][]byte, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("that isn't a Loomplane backup: %w", err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read the backup: %w", err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, err
		}
		files[h.Name] = b
	}
	var m Manifest
	if err := json.Unmarshal(files[manifestFile], &m); err != nil || m.Format != Format {
		return nil, nil, fmt.Errorf("that isn't a %s backup", Format)
	}
	return &m, files, nil
}

// Inspect reads a backup's manifest without restoring it.
func Inspect(r io.Reader) (*Manifest, error) {
	m, _, err := read(r)
	return m, err
}

// --- Migrations and upgrades ---

func orchestrator(ctx context.Context, dsn string, groups []*migrate.Group) (*migrate.Orchestrator, func(), error) {
	db := pgdriver.New()
	if err := db.Open(ctx, dsn); err != nil {
		return nil, nil, fmt.Errorf("connect to the database: %w", err)
	}
	return migrate.NewOrchestrator(pgmigrate.New(db), groups...), func() { _ = db.Close() }, nil
}

// Migrate applies every pending migration.
func Migrate(ctx context.Context, dsn string, groups []*migrate.Group) error {
	o, done, err := orchestrator(ctx, dsn, groups)
	if err != nil {
		return err
	}
	defer done()
	if _, err := o.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Status is where a database's schema stands against this Loomplane.
type Status struct {
	// Applied and Pending are this Loomplane's migrations, as
	// group:version name.
	Applied []string `json:"applied"`
	Pending []string `json:"pending"`
	// Unknown are migrations the database has that this Loomplane
	// doesn't: a newer Loomplane migrated it.
	Unknown []string `json:"unknown"`
}

// SchemaStatus compares the database's migrations with groups.
func SchemaStatus(ctx context.Context, dsn string, groups []*migrate.Group) (*Status, error) {
	o, done, err := orchestrator(ctx, dsn, groups)
	if err != nil {
		return nil, err
	}
	defer done()
	gs, err := o.Status(ctx)
	if err != nil {
		return nil, err
	}
	s := &Status{}
	known := map[string]bool{}
	for _, g := range gs {
		for _, m := range g.Applied {
			s.Applied = append(s.Applied, m.Migration.Group+":"+m.Migration.Version+" "+m.Migration.Name)
			known[m.Migration.Group+":"+m.Migration.Version] = true
		}
		for _, m := range g.Pending {
			s.Pending = append(s.Pending, m.Migration.Group+":"+m.Migration.Version+" "+m.Migration.Name)
			known[m.Migration.Group+":"+m.Migration.Version] = true
		}
	}
	conn, err := connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	applied, err := appliedIn(ctx, conn)
	if err != nil {
		return nil, err
	}
	for _, a := range applied {
		if !known[a] {
			s.Unknown = append(s.Unknown, a)
		}
	}
	sort.Strings(s.Unknown)
	return s, nil
}

// ErrNewerSchema is a database a newer Loomplane migrated.
var ErrNewerSchema = errors.New("a newer Loomplane has migrated this database")

// CheckCompatible refuses a database a newer Loomplane migrated: an older
// one would read and write a schema it doesn't know. Pending migrations
// are fine; starting applies them.
func CheckCompatible(ctx context.Context, dsn string, groups []*migrate.Group) error {
	s, err := SchemaStatus(ctx, dsn, groups)
	if err != nil {
		return err
	}
	if len(s.Unknown) > 0 {
		return fmt.Errorf("%w (it has %s); run that Loomplane, or restore a backup this one made", ErrNewerSchema, strings.Join(s.Unknown, ", "))
	}
	return nil
}

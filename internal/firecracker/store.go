package firecracker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // Registers the embedded durable journal driver.
)

type Store struct {
	db   *sql.DB
	lock *os.File
	mu   sync.Mutex
}

func OpenStore(dir string) (*Store, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == "/" {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrInvalid
	}
	lock, err := lockStore(dir)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.sqlite"))
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(context.Background(), `PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS workloads (id TEXT PRIMARY KEY, body BLOB NOT NULL); CREATE TABLE IF NOT EXISTS routes (hostname TEXT PRIMARY KEY,body BLOB NOT NULL)`); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	if err = os.Chmod(filepath.Join(dir, "state.sqlite"), 0o600); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	return &Store{db: db, lock: lock}, nil
}
func (s *Store) Close() error { return errors.Join(s.db.Close(), s.lock.Close()) }
func (s *Store) Get(ctx context.Context, scope Scope) (Record, error) {
	var raw []byte
	r := Record{}
	err := s.db.QueryRowContext(ctx, "SELECT body FROM workloads WHERE id=?", scope.Key()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrMissing
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	if r.Scope != scope {
		return Record{}, ErrConflict
	}
	return r, nil
}
func (s *Store) Save(ctx context.Context, r Record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO workloads(id,body) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", r.Scope.Key(), raw)
	return err
}
func (s *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM workloads ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var raw []byte
		var r Record
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

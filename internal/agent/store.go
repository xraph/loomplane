// Package agent runs the destination observer. Persisted public state never
// authorizes traffic; each process starts closed and needs a fresh server epoch.
package agent

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/sqlitedriver"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrClosed = errors.New("agent requires fresh authenticated enrollment")

// Store reserves observation numbers durably before publication. SQLite's write
// lock also serializes separate processes; a newer epoch fences an older process.
type Store struct {
	mu       sync.Mutex
	db       *sqlitedriver.SqliteDB
	identity domain.AgentIdentity
	active   bool
	epoch    int64
}

func OpenStore(path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
		return nil, mkdirErr
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrClosed
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if closeErr := file.Close(); closeErr != nil {
			return nil, closeErr
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrClosed
	}
	db := sqlitedriver.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: url.Values{"_pragma": []string{"busy_timeout(5000)", "synchronous(FULL)"}}.Encode()}
	if err := db.Open(ctx, dsn.String(), driver.WithPoolSize(1)); err != nil {
		return nil, err
	}
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS agent_state(id INTEGER PRIMARY KEY CHECK(id=1), epoch INTEGER NOT NULL CHECK(epoch>=0), sequence INTEGER NOT NULL CHECK(sequence>=0), identity_uri TEXT NOT NULL)`,
		`INSERT OR IGNORE INTO agent_state(id,epoch,sequence,identity_uri) VALUES(1,0,0,'')`,
	} {
		if _, err := db.Exec(ctx, query); err != nil {
			return nil, errors.Join(err, db.Close())
		}
	}
	return &Store{db: db}, nil
}

// Activate accepts only the identity returned by a successful enrollment
// handshake. Reopening a state file never restores this in-memory authority.
func (s *Store) Activate(identity domain.AgentIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
	parsed, err := workloadidentity.ParseAgentURI(identity.URI)
	if err != nil || parsed != identity || identity.Epoch > math.MaxInt64 {
		return ErrClosed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var epoch int64
	err = s.db.QueryRow(ctx, `UPDATE agent_state SET epoch=?,sequence=0,identity_uri=? WHERE id=1 AND epoch<? RETURNING epoch`, int64(identity.Epoch), identity.URI, int64(identity.Epoch)).Scan(&epoch)
	if err != nil {
		return errors.Join(ErrClosed, err)
	}
	s.epoch = epoch
	s.identity = identity
	s.active = true
	return nil
}
func (s *Store) NextSequence(ctx context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return 0, ErrClosed
	}
	var sequence int64
	err := s.db.QueryRow(ctx, `UPDATE agent_state SET sequence=sequence+1 WHERE id=1 AND epoch=? AND identity_uri=? AND sequence<? RETURNING sequence`, s.epoch, s.identity.URI, int64(math.MaxInt64)).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		s.active = false
		return 0, ErrClosed
	}
	if err != nil {
		s.active = false
		return 0, err
	}
	if sequence < 1 {
		s.active = false
		return 0, ErrClosed
	}
	return uint64(sequence), nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
	return s.db.Close()
}

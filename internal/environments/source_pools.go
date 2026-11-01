package environments

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/xraph/grove"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// SourcePools reserves independent connections for a source transaction and its
// Vault operation. Gate waiters cannot consume the holder's Vault connection.
// The extension owns these pools from registration through Stop.
type SourcePools struct {
	SQL      *pgdriver.PgDB
	Vault    *grove.DB
	once     sync.Once
	closeErr error
}

func OpenSourcePools(ctx context.Context, dsn string) (*SourcePools, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	open := func() (*pgdriver.PgDB, error) {
		db := pgdriver.New()
		if err := db.Open(ctx, dsn, driver.WithPoolSize(1), driver.WithMaxConnLifetime(30*time.Minute), driver.WithMaxConnIdleTime(5*time.Minute)); err != nil {
			_ = db.Close()
			return nil, err
		}
		if err := db.Ping(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}
	return openSourcePools(open)
}

func openSourcePools(open func() (*pgdriver.PgDB, error)) (*SourcePools, error) {
	source, err := open()
	if err != nil {
		return nil, err
	}
	vault, err := open()
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	wrapped, err := grove.Open(vault)
	if err != nil {
		_ = vault.Close()
		_ = source.Close()
		return nil, err
	}
	return &SourcePools{SQL: source, Vault: wrapped}, nil
}

func (p *SourcePools) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() { p.closeErr = errors.Join(p.Vault.Close(), p.SQL.Close()) })
	return p.closeErr
}

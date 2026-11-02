package cloudinfra

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ClusterPriceError identifies a price blocker without account or capture identities.
type ClusterPriceError struct {
	Reason     string
	Categories []string
}

func (e *ClusterPriceError) Error() string { return e.Reason }
func (e *ClusterPriceError) Unwrap() error { return ErrConflict }
func ClusterAggregateUnsupported(err error) bool {
	var pg *pgconn.PgError
	return errors.Is(err, ErrClusterAggregateUnsupported) || (errors.As(err, &pg) && pg.Code == "23514" && pg.Message == "unsupported cluster control schema")
}

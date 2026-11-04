//go:build cloud

package billing

import (
	"errors"
	"time"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/ledger"

	"github.com/xraph/loomplane/internal/platformcloud"
)

// Service is billing's state and the things it acts through.
type Service struct {
	db        *grove.DB
	pg        *pgdriver.PgDB
	l         *ledger.Ledger
	appID     string
	now       func() time.Time
	payments  Payments
	suspender platformcloud.Suspender
	cfg       Config
	gate      *gate
}

// Deps is what New builds a Service from.
type Deps struct {
	DB        *grove.DB
	Ledger    *ledger.Ledger
	AppID     string
	Now       func() time.Time // nil is time.Now
	Payments  Payments
	Suspender platformcloud.Suspender
	Config    Config
}

// New builds billing's Service. A database that isn't Postgres, or a
// missing ledger or app id, leaves a Service whose every call fails, and
// whose gate refuses everything.
func New(d Deps) *Service {
	s := &Service{
		db: d.DB, l: d.Ledger, appID: d.AppID, now: d.Now,
		payments: d.Payments, suspender: d.Suspender, cfg: d.Config,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if d.DB != nil {
		if pg, ok := d.DB.Driver().(*pgdriver.PgDB); ok {
			s.pg = pg
		}
	}
	// The gate reads the clock through s.now, so a test that swaps the
	// clock moves the cache's clock too.
	s.gate = &gate{account: s.Account, now: func() time.Time { return s.now() }}
	return s
}

// ready says why the Service can't work, or nil.
func (s *Service) ready() error {
	switch {
	case s == nil || s.db == nil || s.pg == nil:
		return errors.New("billing: the service needs Loomplane's Postgres database")
	case s.l == nil:
		return errors.New("billing: the service needs the ledger")
	case s.appID == "":
		return errors.New("billing: the service needs ledger's app id")
	}
	return nil
}

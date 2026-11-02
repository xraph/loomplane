package usage

import (
	"context"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
)

// Spenders are the spend readers for the clouds with a billing API, over
// the given clients.
func Spenders(google *gcp.Client, amazon *aws.Client, microsoft *azure.Client, ocean *digitalocean.Client) map[string]SpendReader {
	return map[string]SpendReader{
		connections.GCP:          googleSpend{google},
		connections.AWS:          awsSpend{amazon},
		connections.Azure:        azureSpend{microsoft},
		connections.DigitalOcean: oceanSpend{ocean},
	}
}

// googleSpend reads the billing export to BigQuery the connection names.
type googleSpend struct{ c *gcp.Client }

func (g googleSpend) Spend(ctx context.Context, conn *connections.Connection, credential []byte, now time.Time) (*metrics.Spend, error) {
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	s, err := g.c.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.Spend(ctx, conn.Details[gcp.BillingExportSetting], now)
}

// awsSpend reads Cost Explorer.
type awsSpend struct{ c *aws.Client }

func (a awsSpend) Spend(ctx context.Context, conn *connections.Connection, credential []byte, now time.Time) (*metrics.Spend, error) {
	key, err := aws.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	return a.c.Session(key, conn.Details["region"], conn.Account).Spend(ctx, now)
}

// azureSpend reads Cost Management on the connection's resource group.
type azureSpend struct{ c *azure.Client }

func (a azureSpend) Spend(ctx context.Context, conn *connections.Connection, credential []byte, now time.Time) (*metrics.Spend, error) {
	cred, err := azure.ParseCredential(credential, conn.Details)
	if err != nil {
		return nil, err
	}
	s, err := a.c.Session(ctx, cred)
	if err != nil {
		return nil, err
	}
	return s.Spend(ctx, now)
}

// oceanSpend reads the team's balance.
type oceanSpend struct{ c *digitalocean.Client }

func (o oceanSpend) Spend(ctx context.Context, _ *connections.Connection, credential []byte, now time.Time) (*metrics.Spend, error) {
	return o.c.MonthToDate(ctx, strings.TrimSpace(string(credential)), now)
}

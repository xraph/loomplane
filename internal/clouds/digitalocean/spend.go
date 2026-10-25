package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

// MonthToDate reads what the team has used this month from the balance.
// The balance has no daily breakdown, so Days is nil. now isn't asked
// about: the balance is always this month's.
func (c *Client) MonthToDate(ctx context.Context, token string, _ time.Time) (*metrics.Spend, error) {
	var out struct {
		Usage string `json:"month_to_date_usage"`
	}
	if err := c.do(ctx, token, http.MethodGet, "/v2/customers/my/balance", nil, nil, &out); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
			return nil, metrics.Unavailablef("Give the token billing read access so Loomplane can read what the team costs")
		}
		return nil, err
	}
	total, err := strconv.ParseFloat(strings.TrimSpace(out.Usage), 64)
	if err != nil {
		return nil, fmt.Errorf("digitalocean answered a month-to-date usage of %q", out.Usage)
	}
	return &metrics.Spend{Currency: "USD", Total: total}, nil
}

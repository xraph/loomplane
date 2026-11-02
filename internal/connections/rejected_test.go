package connections

import (
	"fmt"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/render"
)

func TestAnOutageIsNotARefusalOnRenderOrCloudflare(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		refused bool
	}{
		"render forbids":        {&render.APIError{Status: 403, Message: "forbidden"}, true},
		"render down":           {&render.APIError{Status: 503, Message: "unavailable"}, false},
		"render busy":           {&render.APIError{Status: 429, Message: "slow down"}, false},
		"cloudflare forbids":    {&cloudflare.APIError{Status: 403, Codes: []int{10000}}, true},
		"cloudflare down":       {&cloudflare.APIError{Status: 502}, false},
		"render down, wrapped":  {fmt.Errorf("list services: %w", &render.APIError{Status: 500}), false},
		"cloudflare refuses id": {fmt.Errorf("verify: %w", &cloudflare.APIError{Status: 401}), true},
	} {
		if got := rejected(tc.err); got != tc.refused {
			t.Errorf("%s: rejected %v, want %v", name, got, tc.refused)
		}
	}
}

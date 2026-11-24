package environments

import (
	"errors"
	"testing"
)

func TestAResourceOutputCanNameAWorkspaceService(t *testing.T) {
	for _, ref := range []string{
		"orders", "orders.host", "fabriq.url",
		"providerresource_" + "ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34",
		"endpoint_01jd8x5k2m3n4p5q6r7s8t9v0w", "endpoint_01jd8x5k2m3n4p5q6r7s8t9v0w.password",
	} {
		if err := validateOutput(ref, "", false); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
	for _, ref := range []string{"endpoint_short", "ENDPOINT_01jd8x5k2m3n4p5q6r7s8t9v0w", "postgres://app:pw@db/orders"} {
		if err := validateOutput(ref, "", false); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s was accepted", ref)
		}
	}
}

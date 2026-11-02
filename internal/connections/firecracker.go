package connections

import (
	"context"
	"fmt"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

type FirecrackerChecker struct{}

func (FirecrackerChecker) Parse(raw []byte, settings map[string]string) (Check, error) {
	c, err := fc.NewClient(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	defer c.Close()
	return Check{Account: c.HostID, Details: c.Details}, nil
}
func (FirecrackerChecker) Check(ctx context.Context, raw []byte, settings map[string]string) (Check, error) {
	c, err := fc.NewClient(raw, settings)
	if err != nil {
		return Check{}, err
	}
	defer c.Close()
	if _, err = c.Check(ctx); err != nil {
		return Check{}, err
	}
	return Check{Account: c.HostID, Details: c.Details}, nil
}

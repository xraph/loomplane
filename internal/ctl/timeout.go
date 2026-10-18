package ctl

import (
	"errors"
	"time"

	"github.com/xraph/forge/cli"
)

// timeoutFlag validates before a handler can queue work or contact the API.
func timeoutFlag(description string, fallback time.Duration) cli.Flag {
	return cli.NewDurationFlag("timeout", "", description, fallback, cli.WithValidator(func(value any) error {
		duration, ok := value.(time.Duration)
		if !ok || duration <= 0 {
			return errors.New("timeout must be greater than zero")
		}
		return nil
	}))
}

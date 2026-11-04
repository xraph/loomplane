//go:build cloud

package identity

import (
	authsome "github.com/xraph/authsome"
	"github.com/xraph/grove"
)

// editionOptions are the authsome options only this edition adds. The
// billing options arrive with the billing extension.
func (e *Extension) editionOptions(*grove.DB) ([]authsome.Option, error) { return nil, nil }

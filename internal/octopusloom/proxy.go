package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/recipes"
)

// AdminTarget opens Octopus's admin dashboard through Loomplane's admin
// proxy, on the gateway's private admin port. Every request carries the
// admin API token and nothing else of Loomplane's. In Octopus 0.5.0 the
// X-Octopus-Admin-Token header opens the server-rendered admin pages as
// well as the admin API, so the dashboard never asks for its password.
func AdminTarget() catalogproxy.Target {
	return catalogproxy.Target{
		Component: recipes.OctopusGateway, Port: adminPort,
		Root: "/admin", AllowedPrefixes: []string{"/admin"},
		Header: func(_ context.Context, secret func(name string) ([]byte, error)) (http.Header, error) {
			token, err := secret(adminTokenSecret)
			if err != nil {
				return nil, fmt.Errorf("read Octopus's admin API token: %w", err)
			}
			if len(token) == 0 {
				return nil, errors.New("octopus's admin API token is empty")
			}
			h := http.Header{}
			h.Set(adminTokenHeader, string(token))
			return h, nil
		},
	}
}

func init() { catalogproxy.Register(recipes.OctopusKind, AdminTarget()) }

package fabriqloom

import "github.com/xraph/loomplane/internal/deployments"

// NewTestAdminClient builds a client through the production credential resolver.
func NewTestAdminClient(ch *deployments.ComponentChannel, read func(name string) ([]byte, error)) (*AdminClient, error) {
	admin, _, err := newAdminClient(ch, func() (controlCredential, error) {
		return resolveControlCredential(read)
	})
	return admin, err
}

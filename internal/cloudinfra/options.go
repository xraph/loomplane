package cloudinfra

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func (s *Service) ServerOptions(ctx context.Context, p security.Principal, cloud, connection string) (provider.ServerOptions, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return provider.ServerOptions{}, err
	}
	conn, raw, err := s.Connections.Credential(ctx, p.OrgID, connection)
	if err != nil {
		return provider.ServerOptions{}, err
	}
	return s.readServerOptions(ctx, conn, raw)
}

// optionsTTL is how long AccountServerOptions keeps an account's sizes, and
// optionsFailureTTL how long it keeps a failed read, so a rate-limited
// account isn't asked on every page load.
const (
	optionsTTL        = 10 * time.Minute
	optionsFailureTTL = time.Minute
)

type optionsEntry struct {
	options provider.ServerOptions
	err     error
	at      time.Time
}

// optionsCache keeps AccountServerOptions reads by organization, connection
// and generation, so new credentials are read afresh.
type optionsCache struct {
	mu      sync.Mutex
	entries map[string]optionsEntry
}

// AccountServerOptions is ServerOptions for a connection the caller has
// already authorized, without a Cloud: GPU targets read it to say which
// sizes an account could create. Reads are kept ten minutes per connection
// generation. Callers mustn't change what it returns.
func (s *Service) AccountServerOptions(ctx context.Context, org, connection string) (provider.ServerOptions, error) {
	now := s.now()
	// A ready connection's row names its generation without a Vault
	// reveal, so a cached read never touches the credential.
	if s.RuntimeConnections != nil {
		if c, readErr := s.RuntimeConnections.GetConnection(ctx, org, connection); readErr == nil && c.Status == connections.StatusReady {
			if entry, ok := s.cachedOptions(optionsKey(org, c), now); ok {
				return entry.options, entry.err
			}
		}
	}
	conn, raw, err := s.Connections.Credential(ctx, org, connection)
	if err != nil {
		return provider.ServerOptions{}, err
	}
	key := optionsKey(org, conn)
	if entry, ok := s.cachedOptions(key, now); ok {
		return entry.options, entry.err
	}
	options, err := s.readServerOptions(ctx, conn, raw)
	if ctx.Err() != nil {
		return provider.ServerOptions{}, ctx.Err()
	}
	if s.options != nil {
		s.options.mu.Lock()
		for k, v := range s.options.entries {
			if now.Sub(v.at) >= optionsTTL {
				delete(s.options.entries, k)
			}
		}
		s.options.entries[key] = optionsEntry{options: options, err: err, at: now}
		s.options.mu.Unlock()
	}
	return options, err
}

func optionsKey(org string, c *connections.Connection) string {
	return fmt.Sprintf("%s/%s/%d", org, c.ID, c.Generation)
}

// cachedOptions is the entry for key when it's still fresh at now.
func (s *Service) cachedOptions(key string, now time.Time) (optionsEntry, bool) {
	if s.options == nil {
		return optionsEntry{}, false
	}
	s.options.mu.Lock()
	entry, ok := s.options.entries[key]
	s.options.mu.Unlock()
	fresh := ok && (entry.err == nil && now.Sub(entry.at) < optionsTTL || entry.err != nil && now.Sub(entry.at) < optionsFailureTTL)
	return entry, fresh
}

// readServerOptions reads an account's sizes, regions and images from its provider.
func (s *Service) readServerOptions(ctx context.Context, conn *connections.Connection, raw []byte) (provider.ServerOptions, error) {
	credential, err := Credential(conn, raw)
	if err != nil {
		return provider.ServerOptions{}, err
	}
	adapter, err := s.Adapters.Server(conn.Cloud, provider.ServerProvisioning)
	if err != nil {
		return provider.ServerOptions{}, err
	}
	catalog, ok := adapter.(provider.ServerOptionsAdapter)
	if !ok {
		return provider.ServerOptions{}, provider.ErrInfrastructureUnsupported
	}
	return catalog.ServerOptions(ctx, credential)
}
func validateOptions(options provider.ServerOptions, in ServerInput) error {
	region := false
	for _, v := range options.Regions {
		region = region || v.ID == in.Region
	}
	size := false
	disk := 0
	var chosen *provider.ServerSize
	for i, v := range options.Sizes {
		size = size || (v.ID == in.Size && slices.Contains(v.Regions, in.Region))
		if v.ID == in.Size {
			disk = v.DiskGB
			chosen = &options.Sizes[i]
		}
	}
	// A GPU size needs an image that ships its vendor's driver, whatever else
	// is wrong with the request, so the reviewer learns why first.
	if chosen != nil && chosen.Accelerator != nil {
		if chosen.Accelerator.Vendor == provider.VendorAMD || strings.HasPrefix(chosen.Accelerator.Kind, provider.VendorAMD+"-") {
			return invalidSentence("AMD GPU servers aren't supported yet.")
		}
		if !gpuReadyImage(options, in.Image, *chosen) {
			return gpuImageError(options, in.Region, *chosen)
		}
	}
	image := false
	for _, v := range options.Images {
		image = image || (v.ID == in.Image && v.Bootstrap && v.MinDiskGB >= 0 && v.MinDiskGB <= disk && slices.Contains(v.Regions, in.Region))
	}
	if !region || !size || !image {
		return ErrInvalid
	}
	return nil
}

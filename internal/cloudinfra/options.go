package cloudinfra

import (
	"context"
	"slices"

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
	for _, v := range options.Sizes {
		size = size || (v.ID == in.Size && slices.Contains(v.Regions, in.Region))
	}
	image := false
	for _, v := range options.Images {
		image = image || (v.ID == in.Image && v.Bootstrap && slices.Contains(v.Regions, in.Region))
	}
	if !region || !size || !image {
		return ErrInvalid
	}
	return nil
}

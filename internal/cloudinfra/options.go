package cloudinfra

import (
	"context"
	"slices"
	"strings"

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

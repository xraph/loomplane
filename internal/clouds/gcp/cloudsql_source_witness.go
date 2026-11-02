package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// SQLPSCSourceWitness retains the source fields needed to recover its endpoint
// after the source disappears. It grants no permission to create a resource.
type SQLPSCSourceWitness struct {
	Digest            string `json:"digest"`
	DNSName           string `json:"dns_name"`
	ServiceAttachment string `json:"service_attachment"`
}

func pscSourceScope(project string, spec SQLPSCProvisionSpec, source SQLPSCSourceWitness, subnet SQLApplicationNetwork) string {
	route := subnet.Route
	raw, err := json.Marshal([]string{project, spec.Source.Name, spec.Source.Region, spec.PhysicalID, source.Digest, source.ServiceAttachment, source.DNSName, route.ConsumerProject, route.Network, route.Subnetwork, subnet.ID, subnet.IPCIDR, spec.Endpoint.ForwardingRule})
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func validPSCSourceWitness(source SQLPSCSourceWitness, region string) bool {
	parts := strings.Split(source.ServiceAttachment, "/")
	return validPSCScope(source.Digest) && validSQLPSCName(source.DNSName) && len(parts) == 6 && parts[0] == "projects" && validSQLPathID(parts[1]) && parts[2] == "regions" && parts[3] == region && parts[4] == "serviceAttachments" && sqlNativeName(parts[5])
}

func (s *Session) validPSCLossWitness(spec SQLPSCProvisionSpec, kept SQLPSCProvisionState) bool {
	route := provider.CloudSQLApplicationNetwork{ConsumerProject: spec.Endpoint.ConsumerProject, Network: spec.Endpoint.Network, Subnetwork: spec.Subnetwork}
	prefix, err := netip.ParsePrefix(kept.Subnet.IPCIDR)
	return kept.Version == 3 && kept.SourceProject == s.key.ProjectID && kept.SourcePhysicalID == spec.PhysicalID &&
		s.checkSQLBackupSource(spec.Source.Region, spec.Source.Name, spec.PhysicalID) == nil && spec.Endpoint.Valid(spec.Source.Region) && route.Valid(spec.Source.Region) && kept.Subnet.Route == route && positiveComputeID(kept.Subnet.ID) &&
		err == nil && subnetWithin(prefix, privateSubnetRanges) && validPSCSourceWitness(kept.Source, spec.Source.Region) && kept.Scope == pscSourceScope(s.key.ProjectID, spec, kept.Source, kept.Subnet)
}

func (s *Session) readPSCSourceWitness(ctx context.Context, spec SQLPSCProvisionSpec, kept SQLPSCProvisionState, provision bool) (SQLPSCSourceWitness, error) {
	if err := s.checkSQLBackupSource(spec.Source.Region, spec.Source.Name, spec.PhysicalID); err != nil {
		return SQLPSCSourceWitness{}, err
	}
	in, err := s.GetSQLInstance(ctx, spec.Source.Name)
	if IsNotFound(err) && !provision && s.validPSCLossWitness(spec, kept) {
		return kept.Source, nil
	}
	if err != nil {
		return SQLPSCSourceWitness{}, err
	}
	physical, err := in.Incarnation(s.key.ProjectID, spec.Source.Region, spec.Source.Name)
	if err != nil || physical != spec.PhysicalID {
		return SQLPSCSourceWitness{}, errPSCProvision
	}
	digest, err := in.PITRIsolation(spec.Source)
	if err != nil {
		return SQLPSCSourceWitness{}, err
	}
	if !in.Settings.IPConfiguration.PSC.manualSourceConsumer(spec.Endpoint.ConsumerProject) {
		return SQLPSCSourceWitness{}, errPSCProvision
	}
	source := SQLPSCSourceWitness{Digest: digest, DNSName: in.DNSName, ServiceAttachment: computeResource(in.PSCServiceAttachment)}
	if !validPSCSourceWitness(source, spec.Source.Region) {
		return SQLPSCSourceWitness{}, errPSCProvision
	}
	return source, nil
}

func (s *Session) readPSCSubnetWitness(ctx context.Context, spec SQLPSCProvisionSpec, kept SQLPSCProvisionState, check func(context.Context) error, provision bool) (*SQLApplicationNetwork, error) {
	route := provider.CloudSQLApplicationNetwork{ConsumerProject: spec.Endpoint.ConsumerProject, Network: spec.Endpoint.Network, Subnetwork: spec.Subnetwork}
	subnet, err := s.readPSCSourceSubnet(ctx, spec.Source.Region, route, check)
	if IsNotFound(err) && !provision && s.validPSCLossWitness(spec, kept) {
		return &kept.Subnet, check(ctx)
	}
	return subnet, err
}

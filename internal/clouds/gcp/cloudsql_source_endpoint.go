package gcp

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/manageddb"
)

// SQLSourceEndpoint is an observed endpoint and DNS witness, not SQL proof.
type SQLSourceEndpoint struct {
	ID, Address, DNSName, SourcePhysicalID, SettingsDigest string
}

type sqlManualForwardingRule struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	SelfLink string `json:"selfLink"`
	Region   string `json:"region"`
	Network  string `json:"network"`
	Target   string `json:"target"`
	Address  string `json:"IPAddress"`
	Status   string `json:"pscConnectionStatus"`
}

func computeResource(value string) string {
	for _, prefix := range []string{"https://www.googleapis.com/compute/v1/", "https://compute.googleapis.com/compute/v1/"} {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	if strings.HasPrefix(value, "projects/") {
		return value
	}
	return ""
}

func validSQLPSCName(name string) bool {
	name = strings.TrimSuffix(name, ".")
	if len(name) > 253 || !strings.HasSuffix(name, ".sql.goog") && !strings.HasSuffix(name, ".sql-psc.goog") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' || strings.IndexFunc(label, func(r rune) bool { return r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) >= 0 {
			return false
		}
	}
	return true
}

func (r sqlManualForwardingRule) matches(endpoint manageddb.CloudSQLSourceEndpoint, region, attachment string) bool {
	id, idErr := strconv.ParseUint(r.ID, 10, 64)
	address, addressErr := netip.ParseAddr(r.Address)
	parts := strings.Split(endpoint.ForwardingRule, "/")
	return idErr == nil && id > 0 && strconv.FormatUint(id, 10) == r.ID && addressErr == nil && address.Is4() && address.IsPrivate() && r.Kind == "compute#forwardingRule" && r.Name == parts[5] && computeResource(r.SelfLink) == endpoint.ForwardingRule && computeResource(r.Region) == "projects/"+endpoint.ConsumerProject+"/regions/"+region && computeResource(r.Network) == endpoint.Network && computeResource(r.Target) == attachment && r.Status == "ACCEPTED"
}

// ReadSQLPITRSourceEndpoint verifies an external manual endpoint without changing
// its source, network, DNS or forwarding rule. Reads around DNS resolution fence
// observed source/settings/endpoint replacements. The caller still verifies SQL
// through the explicit PSC connector before publishing connection credentials.
func (s *Session) ReadSQLPITRSourceEndpoint(ctx context.Context, spec SQLInstanceSpec, physical string, endpoint manageddb.CloudSQLSourceEndpoint, check func(context.Context) error) (*SQLSourceEndpoint, error) {
	if !endpoint.Valid(spec.Region) || check == nil || s.c.resolver == nil {
		return nil, ErrSQLPITRUnavailable
	}
	if guardErr := check(ctx); guardErr != nil {
		return nil, guardErr
	}
	in, digest, err := s.readSQLPITRSource(ctx, spec.Region, spec.Name, physical, spec)
	if err != nil {
		return nil, err
	}
	attachment := computeResource(in.PSCServiceAttachment)
	parts := strings.Split(attachment, "/")
	if len(parts) != 6 || parts[0] != "projects" || !validSQLPathID(parts[1]) || parts[2] != "regions" || parts[3] != spec.Region || parts[4] != "serviceAttachments" || !sqlNativeName(parts[5]) || !validSQLPSCName(in.DNSName) || !in.Settings.IPConfiguration.PSC.manualSourceConsumer(endpoint.ConsumerProject) {
		return nil, ErrSQLPITRUnavailable
	}
	readEndpoint := func() (sqlManualForwardingRule, error) {
		var rule sqlManualForwardingRule
		if guardErr := check(ctx); guardErr != nil {
			return rule, guardErr
		}
		if readErr := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+endpoint.ForwardingRule), nil, &rule); readErr != nil {
			return rule, readErr
		}
		if !rule.matches(endpoint, spec.Region, attachment) {
			return rule, ErrSQLPITRUnavailable
		}
		return rule, nil
	}
	rule, err := readEndpoint()
	if err != nil {
		return nil, err
	}
	addresses, err := s.c.resolver.LookupNetIP(ctx, "ip", in.DNSName)
	if err != nil || len(addresses) == 0 {
		return nil, ErrSQLPITRUnavailable
	}
	for _, address := range addresses {
		if !address.Is4() || address.String() != rule.Address {
			return nil, ErrSQLPITRUnavailable
		}
	}
	freshRule, err := readEndpoint()
	if err != nil {
		return nil, err
	}
	if freshRule != rule {
		return nil, ErrSQLPITRUnavailable
	}
	if guardErr := check(ctx); guardErr != nil {
		return nil, guardErr
	}
	freshSource, freshDigest, err := s.readSQLPITRSource(ctx, spec.Region, spec.Name, physical, spec)
	if err != nil {
		return nil, err
	}
	if freshDigest != digest || freshSource.PSCServiceAttachment != in.PSCServiceAttachment || freshSource.DNSName != in.DNSName {
		return nil, ErrSQLPITRUnavailable
	}
	if guardErr := check(ctx); guardErr != nil {
		return nil, guardErr
	}
	return &SQLSourceEndpoint{ID: rule.ID, Address: rule.Address, DNSName: in.DNSName, SourcePhysicalID: physical, SettingsDigest: digest}, nil
}

package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/pkg/provider"

	"github.com/xraph/loomplane/internal/manageddb"
)

// SQLPSCProvisionSpec binds source endpoint creation to a reviewed private route.
// The Cloud SQL source already exists and must remain isolated for PITR.
type SQLPSCProvisionSpec struct {
	Source     SQLInstanceSpec
	PhysicalID string
	Endpoint   manageddb.CloudSQLSourceEndpoint
	Subnetwork string
}

// PSCComputeState retains the exact Compute request, operation and incarnation.
type PSCComputeState struct {
	Attempted   bool   `json:"attempted,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	Operation   string `json:"operation,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	ResourceID  string `json:"resource_id,omitempty"`
	Complete    bool   `json:"complete,omitempty"`
}

// SQLPSCProvisionState is persisted before each owned network mutation.
type SQLPSCProvisionState struct {
	SourceProject    string                `json:"source_project"`
	SourcePhysicalID string                `json:"source_physical_id"`
	Version          int                   `json:"version"`
	Scope            string                `json:"scope"`
	Subnet           SQLApplicationNetwork `json:"subnet"`
	Address          PSCComputeState       `json:"address"`
	ForwardingRule   PSCComputeState       `json:"forwarding_rule"`
	DNS              SQLPSCSourceDNSState  `json:"dns,omitempty"`
}

// SQLPSCProvisionedEndpoint identifies owned network resources. DNS and SQL
// verification are separate gates before publishing a source credential.
type SQLPSCProvisionedEndpoint struct {
	AddressID, ForwardingRuleID, Address, DNSName, ServiceAttachment string
}

var errPSCProvision = errors.New("cloud sql PSC endpoint provisioning does not match its saved source and route")

// EnsureSQLPSCSourceEndpoint provisions the owned private route and its DNS
// records. Resolver reachability and SQL authentication remain separate gates.
func (s *Session) EnsureSQLPSCSourceEndpoint(ctx context.Context, spec SQLPSCProvisionSpec, checkpoint *SQLPSCProvisionState, save func(SQLPSCProvisionState) error, check func(context.Context) error) (*SQLPSCProvisionedEndpoint, error) {
	if checkpoint != nil && checkpoint.DNS != (SQLPSCSourceDNSState{}) && (!checkpoint.Address.Complete || !checkpoint.ForwardingRule.Complete) {
		return nil, errPSCProvision
	}
	endpoint, err := s.EnsureSQLPSCSourceForwardingRule(ctx, spec, checkpoint, save, check)
	if err != nil {
		return nil, err
	}
	guard := func(ctx context.Context) error {
		witness := *checkpoint
		observed, readErr := s.EnsureSQLPSCSourceForwardingRule(ctx, spec, &witness, func(SQLPSCProvisionState) error { return errPSCProvision }, check)
		if readErr != nil {
			return readErr
		}
		if *observed != *endpoint {
			return errPSCProvision
		}
		return nil
	}
	dns := checkpoint.DNS
	saveDNS := func(next SQLPSCSourceDNSState) error {
		updated := *checkpoint
		updated.DNS = next
		if saveErr := save(updated); saveErr != nil {
			return saveErr
		}
		*checkpoint = updated
		return nil
	}
	if dnsErr := s.ensurePSCSourceDNS(ctx, checkpoint.Scope, spec.Endpoint.ConsumerProject, spec.Endpoint.Network, *endpoint, &dns, saveDNS, guard); dnsErr != nil {
		return nil, dnsErr
	}
	return endpoint, nil
}

// EnsureSQLPSCSourceForwardingRule reserves an address and creates a forwarding
// rule without enabling inherited SQL endpoint automation or public ingress.
func (s *Session) EnsureSQLPSCSourceForwardingRule(ctx context.Context, spec SQLPSCProvisionSpec, checkpoint *SQLPSCProvisionState, save func(SQLPSCProvisionState) error, check func(context.Context) error) (*SQLPSCProvisionedEndpoint, error) {
	route := provider.CloudSQLApplicationNetwork{ConsumerProject: spec.Endpoint.ConsumerProject, Network: spec.Endpoint.Network, Subnetwork: spec.Subnetwork}
	if checkpoint == nil || save == nil || check == nil || !spec.Endpoint.Valid(spec.Source.Region) || !route.Valid(spec.Source.Region) {
		return nil, errPSCProvision
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	source, digest, err := s.readSQLPITRSource(ctx, spec.Source.Region, spec.Source.Name, spec.PhysicalID, spec.Source)
	if err != nil {
		return nil, err
	}
	if !source.Settings.IPConfiguration.PSC.manualSourceConsumer(route.ConsumerProject) || !validSQLPSCName(source.DNSName) {
		return nil, errPSCProvision
	}
	attachment := computeResource(source.PSCServiceAttachment)
	parts := strings.Split(attachment, "/")
	if len(parts) != 6 || parts[0] != "projects" || !validSQLPathID(parts[1]) || parts[2] != "regions" || parts[3] != spec.Source.Region || parts[4] != "serviceAttachments" || !sqlNativeName(parts[5]) {
		return nil, errPSCProvision
	}
	subnet, err := s.readPSCSourceSubnet(ctx, spec.Source.Region, route, check)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal([]string{s.key.ProjectID, spec.Source.Name, spec.Source.Region, spec.PhysicalID, digest, attachment, source.DNSName, route.ConsumerProject, route.Network, route.Subnetwork, subnet.ID, subnet.IPCIDR, spec.Endpoint.ForwardingRule})
	if err != nil {
		return nil, err
	}
	scope := fmt.Sprintf("%x", sha256.Sum256(raw))
	state := *checkpoint
	persist := func(next SQLPSCProvisionState) error {
		if err := check(ctx); err != nil {
			return err
		}
		if err := save(next); err != nil {
			return err
		}
		state, *checkpoint = next, next
		return nil
	}
	if state == (SQLPSCProvisionState{}) {
		if err := persist(SQLPSCProvisionState{Version: 2, Scope: scope, SourceProject: s.key.ProjectID, SourcePhysicalID: spec.PhysicalID, Subnet: *subnet}); err != nil {
			return nil, err
		}
	} else {
		if (state.Version != 1 && state.Version != 2) || state.Scope != scope || state.Subnet != *subnet {
			return nil, errPSCProvision
		}
		if state.Version == 1 {
			if state.SourceProject != "" || state.SourcePhysicalID != "" {
				return nil, errPSCProvision
			}
			next := state
			next.Version = 2
			next.SourceProject = s.key.ProjectID
			next.SourcePhysicalID = spec.PhysicalID
			if err := persist(next); err != nil {
				return nil, err
			}
		} else if state.SourceProject != s.key.ProjectID || state.SourcePhysicalID != spec.PhysicalID {
			return nil, errPSCProvision
		}
	}

	guard := func(ctx context.Context) error {
		if err := check(ctx); err != nil {
			return err
		}
		fresh, freshDigest, err := s.readSQLPITRSource(ctx, spec.Source.Region, spec.Source.Name, spec.PhysicalID, spec.Source)
		if err != nil {
			return err
		}
		if freshDigest != digest || fresh.DNSName != source.DNSName || computeResource(fresh.PSCServiceAttachment) != attachment {
			return errPSCProvision
		}
		current, err := s.readPSCSourceSubnet(ctx, spec.Source.Region, route, check)
		if err != nil {
			return err
		}
		if *current != *subnet {
			return errPSCProvision
		}
		return check(ctx)
	}
	prefix := "projects/" + route.ConsumerProject + "/regions/" + spec.Source.Region
	addressName := "lp-psc-" + scope[:24]
	addressPath := prefix + "/addresses/" + addressName
	description := "loomplane-psc:" + scope
	var address pscSourceAddress
	readAddress := func(id string) error {
		address = pscSourceAddress{}
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+addressPath), nil, &address); err != nil {
			return err
		}
		if !address.matches(id, addressName, addressPath, prefix, description, spec, subnet.IPCIDR) {
			return errPSCProvision
		}
		return nil
	}
	addressBody := map[string]any{"name": addressName, "description": description, "addressType": "INTERNAL", "purpose": "GCE_ENDPOINT", "ipVersion": "IPV4", "subnetwork": route.Subnetwork, "networkTier": "PREMIUM"}
	if err := s.ensurePSCCompute(ctx, prefix, addressPath, scope, state.Address, addressBody, readAddress, func(next PSCComputeState) error { updated := state; updated.Address = next; return persist(updated) }, guard); err != nil {
		return nil, err
	}
	var rule sqlManualForwardingRule
	readRule := func(id string) error {
		var observed struct {
			sqlManualForwardingRule
			Description  string `json:"description"`
			GlobalAccess bool   `json:"allowPscGlobalAccess"`
		}
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+spec.Endpoint.ForwardingRule), nil, &observed); err != nil {
			return err
		}
		if observed.ID != id || observed.Description != description || observed.GlobalAccess || observed.Address != address.Address {
			return errPSCProvision
		}
		rule = observed.sqlManualForwardingRule
		return nil
	}
	ruleParts := strings.Split(spec.Endpoint.ForwardingRule, "/")
	ruleBody := map[string]any{"name": ruleParts[5], "description": description, "network": route.Network, "target": attachment, "IPAddress": addressPath, "allowPscGlobalAccess": false}
	ruleGuard := func(ctx context.Context) error {
		if err := guard(ctx); err != nil {
			return err
		}
		if err := readAddress(state.Address.ResourceID); err != nil {
			return err
		}
		return check(ctx)
	}
	if err := s.ensurePSCCompute(ctx, prefix, spec.Endpoint.ForwardingRule, scope, state.ForwardingRule, ruleBody, readRule, func(next PSCComputeState) error {
		updated := state
		updated.ForwardingRule = next
		return persist(updated)
	}, ruleGuard); err != nil {
		return nil, err
	}
	for {
		if err := guard(ctx); err != nil {
			return nil, err
		}
		if err := readAddress(state.Address.ResourceID); err != nil {
			return nil, err
		}
		if err := readRule(state.ForwardingRule.ResourceID); err != nil {
			return nil, err
		}
		pending := rule.Status == "PENDING"
		checked := rule
		if pending {
			checked.Status = "ACCEPTED"
		}
		if !checked.matches(spec.Endpoint, spec.Source.Region, attachment) {
			return nil, errPSCProvision
		}
		if err := guard(ctx); err != nil {
			return nil, err
		}
		if !pending {
			return &SQLPSCProvisionedEndpoint{AddressID: address.ID, ForwardingRuleID: rule.ID, Address: address.Address, DNSName: source.DNSName, ServiceAttachment: attachment}, nil
		}
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
	}
}

func (s *Session) readPSCSourceSubnet(ctx context.Context, region string, route provider.CloudSQLApplicationNetwork, check func(context.Context) error) (*SQLApplicationNetwork, error) {
	var first sqlApplicationSubnet
	for n := range 2 {
		if err := check(ctx); err != nil {
			return nil, err
		}
		var subnet sqlApplicationSubnet
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+route.Subnetwork), nil, &subnet); err != nil {
			return nil, err
		}
		prefix, err := netip.ParsePrefix(subnet.IPCIDR)
		if err != nil || !subnetWithin(prefix, privateSubnetRanges) || !subnet.matchesRoute(route, region) || n > 0 && subnet != first {
			return nil, errPSCProvision
		}
		first = subnet
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	return &SQLApplicationNetwork{Route: route, ID: first.ID, IPCIDR: first.IPCIDR}, nil
}

type pscSourceAddress struct {
	Kind        string   `json:"kind"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	SelfLink    string   `json:"selfLink"`
	Region      string   `json:"region"`
	Description string   `json:"description"`
	Address     string   `json:"address"`
	AddressType string   `json:"addressType"`
	Purpose     string   `json:"purpose"`
	Subnetwork  string   `json:"subnetwork"`
	IPVersion   string   `json:"ipVersion"`
	NetworkTier string   `json:"networkTier"`
	Status      string   `json:"status"`
	Users       []string `json:"users"`
}

func (a pscSourceAddress) matches(id, name, path, prefix, description string, spec SQLPSCProvisionSpec, cidr string) bool {
	ip, ipErr := netip.ParseAddr(a.Address)
	network, networkErr := netip.ParsePrefix(cidr)
	if !positiveComputeID(id) || a.ID != id || a.Kind != "compute#address" || a.Name != name || computeResource(a.SelfLink) != path || computeResource(a.Region) != prefix || a.Description != description || a.AddressType != "INTERNAL" || a.Purpose != "GCE_ENDPOINT" || computeResource(a.Subnetwork) != spec.Subnetwork || !slices.Contains([]string{"", "IPV4"}, a.IPVersion) || !slices.Contains([]string{"", "PREMIUM"}, a.NetworkTier) || ipErr != nil || !ip.Is4() || !ip.IsPrivate() || networkErr != nil || !network.Contains(ip) {
		return false
	}
	if a.Status == "RESERVED" {
		return len(a.Users) == 0
	}
	return a.Status == "IN_USE" && len(a.Users) == 1 && computeResource(a.Users[0]) == spec.Endpoint.ForwardingRule
}

func positiveComputeID(id string) bool {
	value, err := strconv.ParseUint(id, 10, 64)
	return err == nil && value > 0 && strconv.FormatUint(value, 10) == id
}

type pscComputeOperation struct {
	computeOperation
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	RequestID string `json:"clientOperationId"`
	Type      string `json:"operationType"`
	Target    string `json:"targetLink"`
	TargetID  string `json:"targetId"`
}

func (op pscComputeOperation) matches(prefix, path, request string, state PSCComputeState) bool {
	return op.matchesMutation(prefix, path, request, "insert", state)
}

func (op pscComputeOperation) matchesMutation(prefix, path, request, verb string, state PSCComputeState) bool {
	return op.Kind == "compute#operation" && positiveComputeID(op.ID) && validSQLPathID(op.Name) && op.RequestID == request && op.Type == verb && computeResource(op.Target) == path && computeResource(op.Region) == prefix && slices.Contains([]string{"PENDING", "RUNNING", "DONE"}, op.Status) && (state.Operation == "" || state.Operation == op.Name && state.OperationID == op.ID) && (state.ResourceID == "" || state.ResourceID == op.TargetID) && (op.TargetID == "" && op.Status != "DONE" || positiveComputeID(op.TargetID))
}

func (s *Session) ensurePSCCompute(ctx context.Context, prefix, path, scope string, state PSCComputeState, body map[string]any, verify func(string) error, save func(PSCComputeState) error, check func(context.Context) error) error {
	request := uuid.NewSHA1(uuid.NameSpaceURL, []byte(scope+"/"+path)).String()
	if state.RequestID != "" && state.RequestID != request || !state.Attempted && state != (PSCComputeState{}) || state.Attempted && state.RequestID == "" || state.Operation == "" && (state.OperationID != "" || state.ResourceID != "" || state.Complete) || state.Operation != "" && (!validSQLPathID(state.Operation) || !positiveComputeID(state.OperationID)) || state.ResourceID != "" && !positiveComputeID(state.ResourceID) || state.Complete && state.ResourceID == "" {
		return errPSCProvision
	}
	if err := check(ctx); err != nil {
		return err
	}
	if state.Complete {
		if err := verify(state.ResourceID); err != nil {
			return err
		}
		return check(ctx)
	}
	persist := func(next PSCComputeState) error {
		if err := check(ctx); err != nil {
			return err
		}
		if err := save(next); err != nil {
			return err
		}
		state = next
		return nil
	}
	var op pscComputeOperation
	switch {
	case !state.Attempted:
		var existing json.RawMessage
		err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+path), nil, &existing)
		if err == nil {
			return errPSCProvision
		}
		if !IsNotFound(err) {
			return err
		}
		if err := persist(PSCComputeState{Attempted: true, RequestID: request}); err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		collectionPath := path[:strings.LastIndex(path, "/")]
		if err := s.do(ctx, http.MethodPost, s.c.url("compute.googleapis.com", "/compute/v1/"+collectionPath)+"?requestId="+url.QueryEscape(request), body, &op); err != nil {
			return err
		}
	case state.Operation == "":
		var err error
		op, err = s.discoverPSCCompute(ctx, prefix, path, request, check)
		if err != nil {
			return err
		}
	default:
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+prefix+"/operations/"+state.Operation), nil, &op); err != nil {
			return err
		}
	}
	for {
		if !op.matches(prefix, path, request, state) {
			return errPSCProvision
		}
		next := state
		next.Operation, next.OperationID, next.ResourceID = op.Name, op.ID, op.TargetID
		if err := persist(next); err != nil {
			return err
		}
		if op.Error != nil {
			return errors.New("cloud sql PSC Compute operation failed")
		}
		if op.Status == "DONE" {
			if err := verify(state.ResourceID); err != nil {
				return err
			}
			next.Complete = true
			return persist(next)
		}
		if err := s.wait(ctx); err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+prefix+"/operations/"+state.Operation), nil, &op); err != nil {
			return err
		}
	}
}

func (s *Session) discoverPSCCompute(ctx context.Context, prefix, path, request string, check func(context.Context) error) (pscComputeOperation, error) {
	return s.discoverPSCComputeMutation(ctx, prefix, path, request, "insert", PSCComputeState{}, check)
}

func (s *Session) discoverPSCComputeMutation(ctx context.Context, prefix, path, request, verb string, state PSCComputeState, check func(context.Context) error) (pscComputeOperation, error) {
	var found pscComputeOperation
	page := ""
	seen := map[string]bool{}
	for {
		if err := check(ctx); err != nil {
			return found, err
		}
		query := url.Values{"filter": {"clientOperationId = \"" + request + "\""}}
		if page != "" {
			query.Set("pageToken", page)
		}
		var result struct {
			Items []pscComputeOperation `json:"items"`
			Next  string                `json:"nextPageToken"`
		}
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+prefix+"/operations")+"?"+query.Encode(), nil, &result); err != nil {
			return found, err
		}
		for _, op := range result.Items {
			if op.RequestID != request {
				continue
			}
			if found.Name != "" || !op.matchesMutation(prefix, path, request, verb, state) {
				return found, errPSCProvision
			}
			found = op
		}
		if result.Next == "" {
			break
		}
		if seen[result.Next] || len(seen) >= 100 {
			return found, errPSCProvision
		}
		seen[result.Next] = true
		page = result.Next
	}
	if found.Name == "" {
		return found, errors.New("cloud sql PSC mutation is uncertain; its original Compute operation is unavailable")
	}
	return found, nil
}

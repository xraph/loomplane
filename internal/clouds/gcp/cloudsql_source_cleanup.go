package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// PSCDNSRecordCleanup retains the original atomic record deletion.
type PSCDNSRecordCleanup struct {
	RequestID string `json:"request_id,omitempty"`
	ChangeID  string `json:"change_id,omitempty"`
	Complete  bool   `json:"complete,omitempty"`
}

// SQLPSCCleanupState is saved separately from immutable provisioning ownership.
type SQLPSCCleanupState struct {
	Version        int                 `json:"version"`
	Scope          string              `json:"scope"`
	Records        PSCDNSRecordCleanup `json:"records"`
	ZoneRequest    string              `json:"zone_request,omitempty"`
	ZoneComplete   bool                `json:"zone_complete,omitempty"`
	ForwardingRule PSCComputeState     `json:"forwarding_rule"`
	Address        PSCComputeState     `json:"address"`
}

// DeleteSQLPSCSourceEndpoint removes the reconciled resources of an owned
// endpoint. Unattempted stages are skipped. Uncertain creation must first be
// reconciled through its provisioning checkpoint. Cleanup does not read the
// reusable SQL instance name or delete an external manual route.
func (s *Session) DeleteSQLPSCSourceEndpoint(ctx context.Context, spec SQLPSCProvisionSpec, owned SQLPSCProvisionState, endpoint SQLPSCProvisionedEndpoint, checkpoint *SQLPSCCleanupState, save func(SQLPSCCleanupState) error, check func(context.Context) error) error {
	if checkpoint == nil || save == nil || check == nil {
		return errPSCProvision
	}
	if err := s.ValidateSQLPSCCleanupOwnership(spec, owned, endpoint); err != nil {
		return err
	}
	raw, err := json.Marshal([]any{s.key.ProjectID, spec.Source.Name, spec.Source.Region, spec.PhysicalID, spec.Endpoint, spec.Subnetwork, owned, endpoint})
	if err != nil {
		return err
	}
	scope := fmt.Sprintf("%x", sha256.Sum256(raw))
	state := *checkpoint
	if state != (SQLPSCCleanupState{}) && (state.Version != 1 || state.Scope != scope || state.Records.RequestID != "" && !validPSCRequest(state.Records.RequestID) || state.Records.ChangeID != "" && (!validSQLPathID(state.Records.ChangeID) || state.Records.RequestID == "") || state.ZoneRequest != "" && (!validPSCRequest(state.ZoneRequest) || !state.Records.Complete) || state.ZoneComplete && !state.Records.Complete || state.ForwardingRule.Attempted && owned.DNS.ZoneID != "" && !state.ZoneComplete || state.Address.Attempted && owned.ForwardingRule.Complete && !state.ForwardingRule.Complete) {
		return errPSCProvision
	}
	if !validPSCComputeDeletion(state.Address, endpoint.AddressID) || !validPSCComputeDeletion(state.ForwardingRule, endpoint.ForwardingRuleID) {
		return errPSCProvision
	}
	if !owned.Address.Complete && state.Address != (PSCComputeState{}) || !owned.ForwardingRule.Complete && state.ForwardingRule != (PSCComputeState{}) || owned.DNS.ZoneID == "" && (state.Records != (PSCDNSRecordCleanup{}) || state.ZoneRequest != "" || state.ZoneComplete) || !owned.DNS.Complete && (state.Records.RequestID != "" || state.Records.ChangeID != "") {
		return errPSCProvision
	}
	persist := func(next SQLPSCCleanupState) error {
		if err := check(ctx); err != nil {
			return err
		}
		if err := save(next); err != nil {
			return err
		}
		state, *checkpoint = next, next
		return nil
	}
	if err := check(ctx); err != nil {
		return err
	}
	if state.Scope == "" {
		if err := persist(SQLPSCCleanupState{Version: 1, Scope: scope}); err != nil {
			return err
		}
	}
	prefix := "projects/" + spec.Endpoint.ConsumerProject + "/regions/" + spec.Source.Region
	addressName := "lp-psc-" + owned.Scope[:24]
	addressPath := prefix + "/addresses/" + addressName
	description := "loomplane-psc:" + owned.Scope
	var address pscSourceAddress
	readAddress := func() error {
		if err := check(ctx); err != nil {
			return err
		}
		address = pscSourceAddress{}
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+addressPath), nil, &address); err != nil {
			return err
		}
		if !address.matches(endpoint.AddressID, addressName, addressPath, prefix, description, spec, owned.Subnet.IPCIDR) || address.Address != endpoint.Address {
			return errPSCProvision
		}
		return check(ctx)
	}
	readRule := func() error {
		if err := check(ctx); err != nil {
			return err
		}
		var rule struct {
			sqlManualForwardingRule
			Description  string `json:"description"`
			GlobalAccess bool   `json:"allowPscGlobalAccess"`
		}
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+spec.Endpoint.ForwardingRule), nil, &rule); err != nil {
			return err
		}
		// A removed SQL source can leave its owned endpoint closed or pending.
		if rule.Status != "ACCEPTED" && rule.Status != "CLOSED" && rule.Status != "PENDING" {
			return errPSCProvision
		}
		rule.Status = "ACCEPTED"
		if rule.ID != endpoint.ForwardingRuleID || rule.Description != description || rule.GlobalAccess || rule.Address != endpoint.Address || !rule.matches(spec.Endpoint, spec.Source.Region, endpoint.ServiceAttachment) {
			return errPSCProvision
		}
		return check(ctx)
	}
	// Check every remaining Compute object before removing the working DNS route.
	var reads []func() error
	if owned.ForwardingRule.Complete {
		reads = append(reads, readRule)
	}
	if owned.Address.Complete {
		reads = append(reads, readAddress)
	}
	for _, read := range reads {
		if err := read(); err != nil && !IsNotFound(err) {
			return err
		}
	}
	if err := s.deletePSCSourceDNS(ctx, spec.Endpoint.ConsumerProject, spec.Endpoint.Network, owned.DNS, endpoint, &state, persist, check); err != nil {
		return err
	}
	if owned.ForwardingRule.Complete {
		if err := s.deletePSCCompute(ctx, prefix, spec.Endpoint.ForwardingRule, endpoint.ForwardingRuleID, state.ForwardingRule, readRule, func(next PSCComputeState) error {
			updated := state
			updated.ForwardingRule = next
			return persist(updated)
		}, check); err != nil {
			return err
		}
	}

	readUnusedAddress := func() error {
		if err := readAddress(); err != nil {
			return err
		}
		if address.Status != "RESERVED" || len(address.Users) != 0 {
			return errPSCProvision
		}
		return nil
	}
	if owned.Address.Complete {
		if err := s.deletePSCCompute(ctx, prefix, addressPath, endpoint.AddressID, state.Address, readUnusedAddress, func(next PSCComputeState) error { updated := state; updated.Address = next; return persist(updated) }, check); err != nil {
			return err
		}
	}

	return check(ctx)
}

func validPSCDeletionOwnership(spec SQLPSCProvisionSpec, owned SQLPSCProvisionState, endpoint SQLPSCProvisionedEndpoint) bool {
	prefix := "projects/" + spec.Endpoint.ConsumerProject + "/regions/" + spec.Source.Region
	addressPath := prefix + "/addresses/lp-psc-" + owned.Scope[:24]
	for _, item := range []struct {
		state    PSCComputeState
		path, id string
	}{{owned.Address, addressPath, endpoint.AddressID}, {owned.ForwardingRule, spec.Endpoint.ForwardingRule, endpoint.ForwardingRuleID}} {
		request := uuid.NewSHA1(uuid.NameSpaceURL, []byte(owned.Scope+"/"+item.path)).String()
		if invalidPSCComputeCreation(item.state, request) || item.state.Attempted && !item.state.Complete || item.id != item.state.ResourceID {
			return false
		}
	}
	if !owned.Address.Complete && (owned.ForwardingRule != (PSCComputeState{}) || endpoint.Address != "") || !owned.ForwardingRule.Complete && owned.DNS != (SQLPSCSourceDNSState{}) {
		return false
	}
	if owned.DNS != (SQLPSCSourceDNSState{}) && (!validPSCScope(owned.DNS.Scope) || invalidPSCSourceDNSState(owned.DNS, owned.DNS.Scope) || owned.DNS.ZoneRequest != "" && owned.DNS.ZoneID == "" || owned.DNS.RecordRequest != "" && !owned.DNS.Complete) {
		return false
	}
	return true
}

func validPSCScope(scope string) bool {
	raw, err := hex.DecodeString(scope)
	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == scope
}

func validPSCComputeDeletion(state PSCComputeState, id string) bool {
	if state == (PSCComputeState{}) {
		return true
	}
	return state.Attempted && validPSCRequest(state.RequestID) && state.ResourceID == id &&
		(state.Operation == "" && state.OperationID == "" || state.Operation != "" && validSQLPathID(state.Operation) && positiveComputeID(state.OperationID))
}

func (s *Session) deletePSCCompute(ctx context.Context, prefix, path, id string, state PSCComputeState, read func() error, save func(PSCComputeState) error, check func(context.Context) error) error {
	request := state.RequestID
	if request == "" {
		request = uuid.NewString()
	}
	if !validPSCComputeDeletion(state, id) {
		return errPSCProvision
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
	absent := func() (bool, error) {
		err := read()
		if IsNotFound(err) {
			return true, check(ctx)
		}
		return false, err
	}
	gone, readErr := absent()
	if readErr != nil {
		return readErr
	}
	if state.Complete {
		if !gone {
			return errPSCProvision
		}
		return check(ctx)
	}
	if gone {
		next := state
		next.Attempted = true
		next.RequestID = request
		next.ResourceID = id
		next.Complete = true
		return persist(next)
	}
	var op pscComputeOperation
	switch {
	case !state.Attempted:
		if persistErr := persist(PSCComputeState{Attempted: true, RequestID: request, ResourceID: id}); persistErr != nil {
			return persistErr
		}
		gone, readErr = absent()
		if readErr != nil {
			return readErr
		}
		if gone {
			next := state
			next.Complete = true
			return persist(next)
		}
		if checkErr := check(ctx); checkErr != nil {
			return checkErr
		}
		if deleteErr := s.do(ctx, http.MethodDelete, s.c.url("compute.googleapis.com", "/compute/v1/"+path)+"?requestId="+url.QueryEscape(request), nil, &op); deleteErr != nil {
			if pscMutationRejected(deleteErr) {
				return errors.Join(deleteErr, persist(PSCComputeState{}))
			}
			return deleteErr
		}
	case state.Operation == "":
		var discoverErr error
		op, discoverErr = s.discoverPSCComputeMutation(ctx, prefix, path, request, "delete", state, check)
		if discoverErr != nil {
			return discoverErr
		}
	default:
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+prefix+"/operations/"+state.Operation), nil, &op); err != nil {
			return err
		}
	}
	for {
		if !op.matchesMutation(prefix, path, request, "delete", state) {
			return errPSCProvision
		}
		next := state
		next.Operation, next.OperationID = op.Name, op.ID
		if err := persist(next); err != nil {
			return err
		}
		if op.Error != nil {
			failed := errors.New("cloud sql PSC Compute deletion failed")
			if op.Status == "DONE" {
				if err := read(); err != nil {
					return errors.Join(failed, err)
				}
				return errors.Join(failed, persist(PSCComputeState{}))
			}
			return failed
		}
		if op.Status == "DONE" {
			gone, readErr = absent()
			if readErr != nil {
				return readErr
			}
			if !gone {
				return errPSCProvision
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

// Only explicit rejections clear mutation intent. Transport failures and server
// errors retain it so a retry must recover the original operation or absence.
func pscMutationRejected(err error) bool {
	var native *APIError
	return errors.As(err, &native) && (native.Status == 400 || native.Status == 401 || native.Status == 403 || native.Status == 409)
}

// ValidateSQLPSCCleanupOwnership checks a recovered prefix before source deletion.
func (s *Session) ValidateSQLPSCCleanupOwnership(spec SQLPSCProvisionSpec, owned SQLPSCProvisionState, endpoint SQLPSCProvisionedEndpoint) error {
	if !spec.Endpoint.Valid(spec.Source.Region) || owned.Version != 2 || owned.SourceProject != s.key.ProjectID || owned.SourcePhysicalID != spec.PhysicalID || s.checkSQLBackupSource(spec.Source.Region, spec.Source.Name, spec.PhysicalID) != nil || !validPSCScope(owned.Scope) || !validPSCDeletionOwnership(spec, owned, endpoint) || !validSQLPSCName(endpoint.DNSName) {
		return errPSCProvision
	}
	route := owned.Subnet.Route
	if !route.Valid(spec.Source.Region) || route.ConsumerProject != spec.Endpoint.ConsumerProject || route.Network != spec.Endpoint.Network || route.Subnetwork != spec.Subnetwork {
		return errPSCProvision
	}
	attachment := strings.Split(endpoint.ServiceAttachment, "/")
	if len(attachment) != 6 || attachment[0] != "projects" || !validSQLPathID(attachment[1]) || attachment[2] != "regions" || attachment[3] != spec.Source.Region || attachment[4] != "serviceAttachments" || !sqlNativeName(attachment[5]) {
		return errPSCProvision
	}
	return nil
}

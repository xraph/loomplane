package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

func (s *Session) deletePSCSourceDNS(ctx context.Context, project, network string, owned SQLPSCSourceDNSState, endpoint SQLPSCProvisionedEndpoint, state *SQLPSCCleanupState, save func(SQLPSCCleanupState) error, check func(context.Context) error) error {
	want := pscDNSZone{Name: "lp-psc-" + owned.Scope[:24], DNSName: strings.TrimSuffix(endpoint.DNSName, ".") + ".", Description: "loomplane-psc:" + owned.Scope, Visibility: "private"}
	zoneURL := s.c.url("dns.googleapis.com", "/dns/v1/projects/"+project+"/managedZones/"+owned.ZoneID)
	guard := func() error {
		if err := check(ctx); err != nil {
			return err
		}
		var zone pscDNSZone
		if err := s.do(ctx, http.MethodGet, zoneURL, nil, &zone); err != nil {
			return err
		}
		if !zone.matches(want, network, owned.ZoneID) {
			return errPSCProvision
		}
		return check(ctx)
	}
	if err := guard(); IsNotFound(err) {
		next := *state
		next.Records.Complete, next.ZoneComplete = true, true
		return save(next)
	} else if err != nil {
		return err
	}
	if state.ZoneComplete {
		return errPSCProvision
	}
	records := pscSourceRecords(want.DNSName, endpoint.Address, owned.RecordRequest)
	if !state.Records.Complete {
		var change pscDNSChange
		switch {
		case state.Records.RequestID == "":
			if err := s.verifyPSCRecords(ctx, zoneURL, want.DNSName, records, guard); err != nil {
				return err
			}
			next := *state
			next.Records.RequestID = uuid.NewString()
			if err := save(next); err != nil {
				return err
			}
			if err := s.verifyPSCRecords(ctx, zoneURL, want.DNSName, records, guard); err != nil {
				return err
			}
			if err := s.do(ctx, http.MethodPost, zoneURL+"/changes?clientOperationId="+url.QueryEscape(state.Records.RequestID), pscDNSChange{Deletions: records}, &change); err != nil {
				if pscMutationRejected(err) {
					next := *state
					next.Records = PSCDNSRecordCleanup{}
					return errors.Join(err, save(next))
				}
				return err
			}
		case state.Records.ChangeID == "":
			var err error
			change, err = s.discoverPSCChangeMatching(ctx, zoneURL, func(c pscDNSChange) bool { return c.matchesMutation(nil, records, "") }, guard)
			if err != nil {
				return err
			}
		default:
			if err := guard(); err != nil {
				return err
			}
			if err := s.do(ctx, http.MethodGet, zoneURL+"/changes/"+state.Records.ChangeID, nil, &change); err != nil {
				return err
			}
		}
		for {
			if !change.matchesMutation(nil, records, state.Records.ChangeID) {
				return errPSCProvision
			}
			next := *state
			next.Records.ChangeID = change.ID
			if err := save(next); err != nil {
				return err
			}
			if change.Status == "done" {
				break
			}
			if err := s.wait(ctx); err != nil {
				return err
			}
			if err := guard(); err != nil {
				return err
			}
			if err := s.do(ctx, http.MethodGet, zoneURL+"/changes/"+state.Records.ChangeID, nil, &change); err != nil {
				return err
			}
		}
		if err := s.verifyPSCRecords(ctx, zoneURL, want.DNSName, nil, guard); err != nil {
			return err
		}
		next := *state
		next.Records.Complete = true
		if err := save(next); err != nil {
			return err
		}
	}
	if state.ZoneRequest == "" {
		next := *state
		next.ZoneRequest = uuid.NewString()
		if err := save(next); err != nil {
			return err
		}
	}
	if err := s.verifyPSCRecords(ctx, zoneURL, want.DNSName, nil, guard); err != nil {
		return err
	}
	// The immutable zone ID makes a resumed DELETE safe against name reuse.
	if err := s.do(ctx, http.MethodDelete, zoneURL+"?clientOperationId="+url.QueryEscape(state.ZoneRequest), nil, nil); err != nil && !IsNotFound(err) {
		return err
	}
	if err := guard(); !IsNotFound(err) {
		if err != nil {
			return err
		}
		return errPSCProvision
	}
	next := *state
	next.ZoneComplete = true
	return save(next)
}

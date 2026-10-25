package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// SQLPSCSourceDNSState retains zone ownership and the atomic record change.
type SQLPSCSourceDNSState struct {
	Scope         string `json:"scope,omitempty"`
	ZoneRequest   string `json:"zone_request,omitempty"`
	ZoneID        string `json:"zone_id,omitempty"`
	RecordRequest string `json:"record_request,omitempty"`
	ChangeID      string `json:"change_id,omitempty"`
	Complete      bool   `json:"complete,omitempty"`
}

type pscDNSZone struct {
	Name        string `json:"name"`
	DNSName     string `json:"dnsName"`
	Description string `json:"description"`
	ID          string `json:"id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Visibility  string `json:"visibility"`
	Private     struct {
		Networks []struct {
			URL string `json:"networkUrl"`
		} `json:"networks"`
		Clusters []json.RawMessage `json:"gkeClusters,omitempty"`
	} `json:"privateVisibilityConfig"`
	Forwarding *json.RawMessage `json:"forwardingConfig,omitempty"`
	Peering    *json.RawMessage `json:"peeringConfig,omitempty"`
	Reverse    *json.RawMessage `json:"reverseLookupConfig,omitempty"`
	Directory  *json.RawMessage `json:"serviceDirectoryConfig,omitempty"`
}

func (z pscDNSZone) matches(want pscDNSZone, network, id string) bool {
	return positiveComputeID(z.ID) && (id == "" || z.ID == id) && z.Kind == "dns#managedZone" && z.Name == want.Name && z.DNSName == want.DNSName && z.Description == want.Description && z.Visibility == "private" && len(z.Private.Networks) == 1 && computeResource(z.Private.Networks[0].URL) == network && len(z.Private.Clusters) == 0 && z.Forwarding == nil && z.Peering == nil && z.Reverse == nil && z.Directory == nil
}

type pscDNSRecord struct {
	Name    string           `json:"name"`
	Type    string           `json:"type"`
	TTL     int              `json:"ttl"`
	Data    []string         `json:"rrdatas"`
	Routing *json.RawMessage `json:"routingPolicy,omitempty"`
}

func (r pscDNSRecord) matches(want pscDNSRecord) bool {
	return r.Name == want.Name && r.Type == want.Type && r.TTL == want.TTL && slices.Equal(r.Data, want.Data) && r.Routing == nil
}

type pscDNSChange struct {
	ID        string         `json:"id,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Status    string         `json:"status,omitempty"`
	Additions []pscDNSRecord `json:"additions,omitempty"`
	Deletions []pscDNSRecord `json:"deletions,omitempty"`
}

func (c pscDNSChange) matches(want []pscDNSRecord, id string) bool {
	return c.matchesMutation(want, nil, id)
}

func (c pscDNSChange) matchesMutation(additions, deletions []pscDNSRecord, id string) bool {
	if c.Kind != "dns#change" || !validSQLPathID(c.ID) || id != "" && c.ID != id || !slices.Contains([]string{"pending", "done"}, c.Status) || len(c.Deletions) != len(deletions) || len(c.Additions) != len(additions) {
		return false
	}
	return pscRecordsMatch(c.Additions, additions) && pscRecordsMatch(c.Deletions, deletions)
}

func pscRecordsMatch(found, want []pscDNSRecord) bool {
	if len(found) != len(want) {
		return false
	}
	for _, record := range want {
		count := 0
		for _, actual := range found {
			if actual.matches(record) {
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	return true
}

func validPSCRequest(id string) bool {
	value, err := uuid.Parse(id)
	return err == nil && value != uuid.Nil && value.String() == id
}

func (s *Session) ensurePSCSourceDNS(ctx context.Context, networkScope, project, network string, endpoint SQLPSCProvisionedEndpoint, checkpoint *SQLPSCSourceDNSState, save func(SQLPSCSourceDNSState) error, check func(context.Context) error) error {
	return s.pscSourceDNS(ctx, networkScope, project, network, endpoint, checkpoint, save, check, true)
}

func (s *Session) pscSourceDNS(ctx context.Context, networkScope, project, network string, endpoint SQLPSCProvisionedEndpoint, checkpoint *SQLPSCSourceDNSState, save func(SQLPSCSourceDNSState) error, check func(context.Context) error, provision bool) error {
	ip, ipErr := netip.ParseAddr(endpoint.Address)
	parts := strings.Split(network, "/")
	if checkpoint == nil || save == nil || check == nil || networkScope == "" || !validSQLPathID(project) || len(parts) != 5 || parts[0] != "projects" || parts[1] != project || parts[2] != "global" || parts[3] != "networks" || !validSQLPathID(parts[4]) || !positiveComputeID(endpoint.AddressID) || !positiveComputeID(endpoint.ForwardingRuleID) || !validSQLPSCName(endpoint.DNSName) || ipErr != nil || !ip.Is4() || !ip.IsPrivate() {
		return errPSCProvision
	}
	raw, marshalErr := json.Marshal([]string{networkScope, project, network, endpoint.AddressID, endpoint.ForwardingRuleID, endpoint.Address, endpoint.DNSName})
	if marshalErr != nil {
		return marshalErr
	}
	scope := fmt.Sprintf("%x", sha256.Sum256(raw))
	state := *checkpoint
	if state != (SQLPSCSourceDNSState{}) && (state.Scope != scope || state.ZoneRequest != "" && !validPSCRequest(state.ZoneRequest) || state.ZoneRequest == "" && (state.ZoneID != "" || state.RecordRequest != "" || state.ChangeID != "" || state.Complete) || state.ZoneID != "" && !positiveComputeID(state.ZoneID) || state.ZoneID == "" && (state.RecordRequest != "" || state.ChangeID != "" || state.Complete) || state.RecordRequest != "" && !validPSCRequest(state.RecordRequest) || state.RecordRequest == "" && (state.ChangeID != "" || state.Complete) || state.ChangeID != "" && !validSQLPathID(state.ChangeID) || state.Complete && state.ChangeID == "") {
		return errPSCProvision
	}
	persist := func(next SQLPSCSourceDNSState) error {
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
		if err := persist(SQLPSCSourceDNSState{Scope: scope}); err != nil {
			return err
		}
	}
	want := pscDNSZone{Name: "lp-psc-" + scope[:24], DNSName: strings.TrimSuffix(endpoint.DNSName, ".") + ".", Description: "loomplane-psc:" + scope, Visibility: "private"}
	want.Private.Networks = append(want.Private.Networks, struct {
		URL string `json:"networkUrl"`
	}{URL: "https://www.googleapis.com/compute/v1/" + network})
	collection := s.c.url("dns.googleapis.com", "/dns/v1/projects/"+project+"/managedZones")
	readZone := func(id string) (pscDNSZone, error) {
		var zone pscDNSZone
		err := s.do(ctx, http.MethodGet, collection+"/"+id, nil, &zone)
		return zone, err
	}
	if state.ZoneID == "" {
		var zone pscDNSZone
		if state.ZoneRequest == "" {
			if !provision {
				return check(ctx)
			}
			_, err := readZone(want.Name)
			if err == nil {
				return errPSCProvision
			}
			if !IsNotFound(err) {
				return err
			}
			next := state
			next.ZoneRequest = uuid.NewString()
			if err := persist(next); err != nil {
				return err
			}
			if err := check(ctx); err != nil {
				return err
			}
			if err := s.do(ctx, http.MethodPost, collection+"?clientOperationId="+url.QueryEscape(state.ZoneRequest), want, &zone); err != nil {
				return err
			}
		} else {
			for {
				if err := check(ctx); err != nil {
					return err
				}
				var op struct {
					ID      string `json:"id"`
					Kind    string `json:"kind"`
					Status  string `json:"status"`
					Type    string `json:"type"`
					Context struct {
						Old *pscDNSZone `json:"oldValue"`
						New pscDNSZone  `json:"newValue"`
					} `json:"zoneContext"`
				}
				if err := s.do(ctx, http.MethodGet, collection+"/"+want.Name+"/operations/"+state.ZoneRequest, nil, &op); err != nil {
					return err
				}
				if op.ID != state.ZoneRequest || op.Kind != "dns#operation" || op.Type != "insert" || op.Context.Old != nil || !slices.Contains([]string{"pending", "done"}, op.Status) || !op.Context.New.matches(want, network, "") {
					return errPSCProvision
				}
				zone = op.Context.New
				if op.Status == "done" {
					break
				}
				if err := s.wait(ctx); err != nil {
					return err
				}
			}
		}
		if !zone.matches(want, network, "") {
			return errPSCProvision
		}
		next := state
		next.ZoneID = zone.ID
		if err := persist(next); err != nil {
			return err
		}
	}
	zoneGuard := func() error {
		if err := check(ctx); err != nil {
			return err
		}
		zone, err := readZone(state.ZoneID)
		if err != nil {
			return err
		}
		if !zone.matches(want, network, state.ZoneID) {
			return errPSCProvision
		}
		return check(ctx)
	}
	zoneURL := collection + "/" + state.ZoneID
	if err := zoneGuard(); err != nil {
		return err
	}
	if state.RecordRequest == "" {
		if err := s.verifyPSCRecords(ctx, zoneURL, want.DNSName, nil, zoneGuard); err != nil {
			return err
		}
		if !provision {
			return zoneGuard()
		}
		next := state
		next.RecordRequest = uuid.NewString()
		if err := persist(next); err != nil {
			return err
		}
		if err := zoneGuard(); err != nil {
			return err
		}
		records := pscSourceRecords(want.DNSName, endpoint.Address, state.RecordRequest)
		var change pscDNSChange
		if err := s.do(ctx, http.MethodPost, zoneURL+"/changes?clientOperationId="+url.QueryEscape(state.RecordRequest), pscDNSChange{Additions: records}, &change); err != nil {
			return err
		}
		if !change.matches(records, "") {
			return errPSCProvision
		}
		next = state
		next.ChangeID = change.ID
		if err := persist(next); err != nil {
			return err
		}
	}
	records := pscSourceRecords(want.DNSName, endpoint.Address, state.RecordRequest)
	if state.ChangeID == "" {
		change, err := s.discoverPSCChange(ctx, zoneURL, records, zoneGuard)
		if err != nil {
			return err
		}
		next := state
		next.ChangeID = change.ID
		if err := persist(next); err != nil {
			return err
		}
	}
	if !state.Complete {
		for {
			if err := zoneGuard(); err != nil {
				return err
			}
			var change pscDNSChange
			if err := s.do(ctx, http.MethodGet, zoneURL+"/changes/"+state.ChangeID, nil, &change); err != nil {
				return err
			}
			if !change.matches(records, state.ChangeID) {
				return errPSCProvision
			}
			if change.Status == "done" {
				break
			}
			if err := s.wait(ctx); err != nil {
				return err
			}
		}
	}
	if err := s.verifyPSCRecords(ctx, zoneURL, want.DNSName, records, zoneGuard); err != nil {
		return err
	}
	if !state.Complete {
		next := state
		next.Complete = true
		if err := persist(next); err != nil {
			return err
		}
	}
	return s.verifyPSCRecords(ctx, zoneURL, want.DNSName, records, zoneGuard)
}

func pscSourceRecords(name, address, request string) []pscDNSRecord {
	return []pscDNSRecord{{Name: name, Type: "A", TTL: 60, Data: []string{address}}, {Name: name, Type: "TXT", TTL: 60, Data: []string{`"loomplane-psc:` + request + `"`}}}
}

func (s *Session) verifyPSCRecords(ctx context.Context, zoneURL, name string, want []pscDNSRecord, check func() error) error {
	var found []pscDNSRecord
	err := s.walkPSCRecordPages(ctx, zoneURL+"/rrsets", check, func(raw json.RawMessage) error {
		var result struct {
			Records []pscDNSRecord `json:"rrsets"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		for _, record := range result.Records {
			if record.Name == name && (record.Type == "NS" || record.Type == "SOA") {
				continue
			}
			found = append(found, record)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !pscRecordsMatch(found, want) {
		return errPSCProvision
	}
	return check()
}

func (s *Session) discoverPSCChange(ctx context.Context, zoneURL string, want []pscDNSRecord, check func() error) (pscDNSChange, error) {
	return s.discoverPSCChangeMatching(ctx, zoneURL, func(change pscDNSChange) bool { return change.matches(want, "") }, check)
}

func (s *Session) discoverPSCChangeMatching(ctx context.Context, zoneURL string, matches func(pscDNSChange) bool, check func() error) (pscDNSChange, error) {
	var found pscDNSChange
	err := s.walkPSCRecordPages(ctx, zoneURL+"/changes", check, func(raw json.RawMessage) error {
		var result struct {
			Changes []pscDNSChange `json:"changes"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		for _, change := range result.Changes {
			if !matches(change) {
				continue
			}
			if found.ID != "" {
				return errPSCProvision
			}
			found = change
		}
		return nil
	})
	if err != nil {
		return found, err
	}
	if found.ID == "" {
		return found, errPSCProvision
	}
	return found, check()
}

func (s *Session) walkPSCRecordPages(ctx context.Context, path string, check func() error, visit func(json.RawMessage) error) error {
	page := ""
	seen := map[string]bool{}
	for {
		if err := check(); err != nil {
			return err
		}
		query := url.Values{"maxResults": {"100"}}
		if page != "" {
			query.Set("pageToken", page)
		}
		var raw json.RawMessage
		if err := s.do(ctx, http.MethodGet, path+"?"+query.Encode(), nil, &raw); err != nil {
			return err
		}
		if err := visit(raw); err != nil {
			return err
		}
		var result struct {
			Next string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		if result.Next == "" {
			return check()
		}
		if seen[result.Next] || len(seen) >= 100 {
			return errPSCProvision
		}
		seen[result.Next] = true
		page = result.Next
	}
}

//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/manageddb"
)

// sourceNetworkFixture serves the Google HTTP boundary while the executor uses
// real checkpoints and PostgreSQL authentication in the source integration test.
type sourceNetworkFixture struct {
	checkpoint          func() []byte
	t                   *testing.T
	endpoint            manageddb.CloudSQLSourceEndpoint
	instance, scenario  string
	objects, operations map[string]map[string]any
	zone                map[string]any
	records             []map[string]any
	changes             map[string]map[string]any
	counts              map[string]int
	lost                bool
}

func newSourceNetworkFixture(t *testing.T, endpoint manageddb.CloudSQLSourceEndpoint, instance, scenario string) *sourceNetworkFixture {
	return &sourceNetworkFixture{t: t, endpoint: endpoint, instance: instance, scenario: scenario, objects: map[string]map[string]any{}, operations: map[string]map[string]any{}, changes: map[string]map[string]any{}, counts: map[string]int{}}
}

func (f *sourceNetworkFixture) serve(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/compute/") && !strings.HasPrefix(r.URL.Path, "/dns/") {
		return false
	}
	if r.Method != http.MethodGet {
		var saved map[string]json.RawMessage
		if f.checkpoint == nil || json.Unmarshal(f.checkpoint(), &saved) != nil {
			f.t.Error("provider mutation has no durable executor checkpoint")
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		request := r.URL.Query().Get("requestId")
		if request == "" {
			request = r.URL.Query().Get("clientOperationId")
		}
		state := saved["psc"]
		if r.Method == http.MethodDelete || strings.HasSuffix(r.URL.Path, "/changes") && f.counts["insert-records"] > 0 {
			state = saved["psc_cleanup"]
		}
		if request == "" || !strings.Contains(string(state), request) {
			f.t.Error("mutation request absent from saved executor progress", r.Method, r.URL.Path)
		}
	}
	reply := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			f.t.Error(err)
		}
	}
	body := func() map[string]any {
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			f.t.Error(err)
		}
		return v
	}
	prefix := "projects/app-project/regions/europe-west1"
	if strings.HasPrefix(r.URL.Path, "/compute/") {
		path := strings.TrimPrefix(r.URL.Path, "/compute/v1/")
		if path == f.endpoint.ProvisionSubnetwork {
			reply(map[string]any{"kind": "compute#subnetwork", "id": "234", "name": "psc", "selfLink": path, "region": prefix, "network": f.endpoint.Network, "ipCidrRange": "10.20.0.0/28", "purpose": "PRIVATE", "state": "READY", "stackType": "IPV4_ONLY"})
			return true
		}
		if strings.HasSuffix(path, "/operations") {
			items := []map[string]any{}
			for _, op := range f.operations {
				items = append(items, op)
			}
			reply(map[string]any{"items": items})
			return true
		}
		if op := f.operations[path]; op != nil {
			reply(op)
			return true
		}
		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			insert := r.Method == http.MethodPost
			kind, id, verb := "rule", "789", "delete"
			if strings.Contains(path, "/addresses") {
				kind, id = "address", "456"
			}
			target := path
			if insert {
				verb = "insert"
				obj := body()
				target += "/" + obj["name"].(string)
				if f.objects[target] != nil {
					f.t.Error("repeated create", target)
				}
				obj["id"], obj["selfLink"], obj["region"] = id, target, prefix
				if kind == "address" {
					obj["kind"], obj["address"], obj["status"] = "compute#address", "10.20.0.8", "RESERVED"
				} else {
					obj["kind"], obj["pscConnectionStatus"], obj["IPAddress"] = "compute#forwardingRule", "ACCEPTED", "10.20.0.8"
				}
				f.objects[target] = obj
			} else {
				if f.objects[path] == nil {
					w.WriteHeader(404)
					return true
				}
				if f.zone != nil {
					f.t.Error("Compute removed before DNS")
				}
				delete(f.objects, path)
			}
			f.counts[verb+"-"+kind]++
			name := verb + "-" + kind
			op := map[string]any{"kind": "compute#operation", "id": "999", "name": name, "region": prefix, "status": "DONE", "operationType": verb, "targetLink": target, "targetId": id, "clientOperationId": r.URL.Query().Get("requestId")}
			f.operations[prefix+"/operations/"+name] = op
			if !f.lost && (f.scenario == "provision-lost-rule" && name == "insert-rule" || f.scenario == "provision-remove-retry" && name == "delete-rule") {
				f.lost = true
				w.WriteHeader(503)
				return true
			}
			reply(op)
			return true
		}
		if obj := f.objects[path]; obj != nil {
			reply(obj)
		} else {
			w.WriteHeader(404)
		}
		return true
	}
	if strings.HasSuffix(r.URL.Path, "/managedZones") && r.Method == http.MethodPost {
		f.zone = body()
		f.zone["id"], f.zone["kind"] = "678", "dns#managedZone"
		f.counts["insert-zone"]++
		reply(f.zone)
		return true
	}
	if f.zone == nil {
		w.WriteHeader(404)
		return true
	}
	if strings.HasSuffix(r.URL.Path, "/rrsets") {
		reply(map[string]any{"rrsets": f.records})
		return true
	}
	if strings.HasSuffix(r.URL.Path, "/changes") {
		if r.Method == http.MethodGet {
			values := []map[string]any{}
			for _, c := range f.changes {
				values = append(values, c)
			}
			reply(map[string]any{"changes": values})
			return true
		}
		change := body()
		id, kind := "1", "insert-records"
		raw, _ := json.Marshal(change["additions"])
		if change["deletions"] != nil {
			f.records = nil
			id, kind = "2", "delete-records"
		} else if err := json.Unmarshal(raw, &f.records); err != nil {
			f.t.Error(err)
		}
		change["id"], change["kind"], change["status"] = id, "dns#change", "done"
		f.changes[id] = change
		f.counts[kind]++
		reply(change)
		return true
	}
	if strings.Contains(r.URL.Path, "/changes/") {
		reply(f.changes[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]])
		return true
	}
	if r.Method == http.MethodDelete {
		if len(f.records) > 0 {
			f.t.Error("zone still has records")
		}
		f.zone = nil
		f.counts["delete-zone"]++
		reply(map[string]any{})
		return true
	}
	reply(f.zone)
	return true
}

func (f *sourceNetworkFixture) assertCreates() {
	f.t.Helper()
	for _, kind := range []string{"address", "rule", "zone", "records"} {
		if f.counts["insert-"+kind] != 1 {
			f.t.Fatal("missing or repeated owned creation", f.counts)
		}
	}
}
func (f *sourceNetworkFixture) assertRemoved() {
	f.t.Helper()
	for _, kind := range []string{"address", "rule", "zone", "records"} {
		if f.counts["delete-"+kind] != 1 {
			f.t.Fatal("missing or repeated owned deletion", f.counts)
		}
	}
	if len(f.objects) != 0 || f.zone != nil || len(f.records) != 0 {
		f.t.Fatal("owned resources leaked")
	}
}

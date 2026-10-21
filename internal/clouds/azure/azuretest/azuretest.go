// Package azuretest is a fake of the parts of Microsoft Entra ID and
// Azure Resource Manager that Loomplane uses, for tests: one tenant, one
// subscription, and the resource group's registry, Container Apps
// environments and container apps. Everything it makes reports
// provisioning once before it succeeds, so pollers are exercised.
//
//nolint:errcheck,gosec // A test fake: its secret is fake, and a request it can't read fails the test that sent it.
package azuretest

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The fake's identities.
const (
	Tenant       = "11111111-1111-1111-1111-111111111111"
	Client       = "22222222-2222-2222-2222-222222222222"
	Subscription = "33333333-3333-3333-3333-333333333333"
	Secret       = "fake~client.secret"
)

// Settings are what a connection to the fake gives.
func Settings() map[string]string {
	return map[string]string{"tenant_id": Tenant, "client_id": Client, "subscription_id": Subscription}
}

// Fake is a running fake.
type Fake struct {
	Server *httptest.Server

	mu sync.Mutex
	// Actions are what the principal may do; a Contributor's by default.
	Actions, NotActions []string
	// FailBuild makes every ACR Tasks run fail.
	FailBuild  bool
	groups     map[string]string // name → location
	registered map[string]bool
	resources  map[string]map[string]any // lower-cased resource ID → body
	polls      map[string]int
	// Uploads are the source tarballs, by relative path.
	Uploads map[string][]byte
	// Tasks are the ACR Tasks files of the runs started, decoded.
	Tasks []string
	// Apps are the container app bodies Loomplane PUT, by name.
	Apps    map[string]map[string]any
	Deleted []string
}

// New starts a fake.
func New(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{
		Actions: []string{"*"}, NotActions: []string{"Microsoft.Authorization/*/Write", "Microsoft.Authorization/*/Delete"},
		groups: map[string]string{}, registered: map[string]bool{}, resources: map[string]map[string]any{},
		polls: map[string]int{}, Uploads: map[string][]byte{}, Apps: map[string]map[string]any{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the fake's address, which replaces Azure's hosts.
func (f *Fake) URL() string { return f.Server.URL }

// Lock and Unlock guard what the fake recorded.
func (f *Fake) Lock()   { f.mu.Lock() }
func (f *Fake) Unlock() { f.mu.Unlock() }

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	notFound := func(code string) {
		reply(http.StatusNotFound, map[string]any{"error": map[string]string{"code": code, "message": r.URL.Path + " not found"}})
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/apps/"):
		w.WriteHeader(http.StatusOK) // a deployed app, for the health check
		return
	case p == "/"+Tenant+"/oauth2/v2.0/token":
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") != Client || r.PostForm.Get("client_secret") != Secret {
			reply(http.StatusUnauthorized, map[string]string{"error": "invalid_client", "error_description": "AADSTS7000215: Invalid client secret provided.\r\nTrace ID: 0"})
			return
		}
		reply(http.StatusOK, map[string]any{"access_token": "fake-token", "expires_in": 3600})
		return
	case strings.HasPrefix(p, "/blobs/") && r.Method == http.MethodPut:
		if r.Header.Get("x-ms-blob-type") != "BlockBlob" || r.Header.Get("Authorization") != "" {
			reply(http.StatusBadRequest, map[string]string{"message": "not a SAS block blob upload"})
			return
		}
		data, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.Uploads[strings.TrimPrefix(p, "/blobs/")] = data
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake-token" {
		reply(http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "InvalidAuthenticationToken", "message": "no token"}})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()

	sub := "/subscriptions/" + Subscription
	lower := strings.ToLower(p)
	parts := strings.Split(strings.Trim(lower, "/"), "/")
	switch {
	case p == sub:
		reply(http.StatusOK, map[string]any{"subscriptionId": Subscription, "tenantId": Tenant, "displayName": "Acme production", "state": "Enabled"})
	case strings.HasSuffix(lower, "/providers/microsoft.authorization/permissions"):
		reply(http.StatusOK, map[string]any{"value": []any{map[string]any{"actions": f.Actions, "notActions": f.NotActions}}})
	case len(parts) == 4 && parts[2] == "providers" && r.Method == http.MethodGet:
		state := "NotRegistered"
		if f.registered[parts[3]] {
			state = "Registered"
		}
		reply(http.StatusOK, map[string]any{"registrationState": state})
	case len(parts) == 5 && parts[2] == "providers" && parts[4] == "register":
		f.registered[parts[3]] = true
		reply(http.StatusOK, map[string]any{"registrationState": "Registering"})
	case len(parts) == 4 && parts[2] == "resourcegroups":
		switch r.Method {
		case http.MethodGet:
			if loc, ok := f.groups[parts[3]]; ok {
				reply(http.StatusOK, map[string]any{"name": parts[3], "location": loc})
				return
			}
			notFound("ResourceGroupNotFound")
		case http.MethodPut:
			f.groups[parts[3]], _ = body["location"].(string)
			reply(http.StatusCreated, map[string]any{"name": parts[3]})
		}
	case len(parts) >= 8 && parts[2] == "resourcegroups" && parts[4] == "providers":
		f.resource(w, r, reply, notFound, p, parts, body)
	default:
		notFound("NotFound")
	}
}

// resource serves what's in a resource group.
func (f *Fake) resource(w http.ResponseWriter, r *http.Request, reply func(int, any), notFound func(string), p string, parts []string, body map[string]any) {
	kind := parts[5] + "/" + parts[6]
	id := "/" + strings.Join(parts[:8], "/")
	name := strings.Split(strings.Trim(p, "/"), "/")[7]
	canonical := strings.Join(strings.Split(p, "/")[:9], "/")
	rest := parts[8:]
	if _, ok := f.groups[parts[3]]; !ok {
		notFound("ResourceGroupNotFound")
		return
	}
	// GET reports provisioning once, then the final state.
	get := func() {
		res := f.resources[id]
		if res == nil {
			notFound("ResourceNotFound")
			return
		}
		props := res["properties"].(map[string]any)
		if f.polls[id] == 0 {
			props["provisioningState"] = "InProgress"
		} else if props["provisioningState"] == "InProgress" {
			props["provisioningState"] = "Succeeded"
			if kind == "microsoft.app/containerapps" {
				props["latestReadyRevisionName"] = props["latestRevisionName"]
			}
		}
		f.polls[id]++
		reply(http.StatusOK, res)
	}
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		get()
	case len(rest) == 0 && r.Method == http.MethodPut:
		props, _ := body["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		body["id"], body["name"], body["properties"] = canonical, name, props
		props["provisioningState"] = "InProgress"
		switch kind {
		case "microsoft.containerregistry/registries":
			props["loginServer"] = name + ".azurecr.io"
		case "microsoft.app/managedenvironments":
			props["defaultDomain"] = "fake.azurecontainerapps.io"
		case "microsoft.app/containerapps":
			f.Apps[name] = body
			rev := 1
			if old := f.resources[id]; old != nil {
				rev = int(old["revision"].(float64)) + 1
			}
			body["revision"] = float64(rev)
			props["latestRevisionName"] = name + "--" + string(rune('0'+rev))
			if conf, _ := props["configuration"].(map[string]any); conf != nil {
				if ing, _ := conf["ingress"].(map[string]any); ing != nil {
					ing["fqdn"] = strings.TrimPrefix(f.Server.URL, "http://") + "/apps/" + name
				}
			}
		}
		f.resources[id], f.polls[id] = body, 0
		reply(http.StatusCreated, body)
	case len(rest) == 0 && r.Method == http.MethodDelete:
		if f.resources[id] == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		delete(f.resources, id)
		f.Deleted = append(f.Deleted, name)
		w.WriteHeader(http.StatusAccepted)
	case kind == "microsoft.containerregistry/registries" && len(rest) == 1 && rest[0] == "listcredentials":
		reply(http.StatusOK, map[string]any{"username": name, "passwords": []any{map[string]string{"name": "password", "value": "registry-pw"}}})
	case kind == "microsoft.containerregistry/registries" && len(rest) == 1 && rest[0] == "listbuildsourceuploadurl":
		rel := "source/" + name + "/" + string(rune('a'+len(f.Uploads))) + ".tar.gz"
		reply(http.StatusOK, map[string]any{"uploadUrl": f.Server.URL + "/blobs/" + rel + "?sv=2019&sig=fake", "relativePath": rel})
	case kind == "microsoft.containerregistry/registries" && len(rest) == 1 && rest[0] == "schedulerun":
		task, _ := base64.StdEncoding.DecodeString(body["encodedTaskContent"].(string))
		if _, ok := f.Uploads[body["sourceLocation"].(string)]; !ok {
			reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "InvalidSource", "message": "nothing uploaded there"}})
			return
		}
		f.Tasks = append(f.Tasks, string(task))
		run := "cb" + string(rune('0'+len(f.Tasks)))
		reply(http.StatusOK, map[string]any{"name": run, "properties": map[string]any{"runId": run, "status": "Queued"}})
	case kind == "microsoft.containerregistry/registries" && len(rest) == 2 && rest[0] == "runs":
		key := id + "/runs/" + rest[1]
		f.polls[key]++
		status := "Running"
		if f.polls[key] > 1 {
			status = "Succeeded"
			if f.FailBuild {
				status = "Failed"
			}
		}
		reply(http.StatusOK, map[string]any{"properties": map[string]any{"runId": rest[1], "status": status, "runErrorMessage": map[bool]string{true: "The command returned a non-zero code: 1"}[status == "Failed"]}})
	case kind == "microsoft.app/containerapps" && len(rest) == 2 && rest[0] == "revisions":
		reply(http.StatusOK, map[string]any{"properties": map[string]any{"provisioningState": "Provisioning", "runningState": "Processing"}})
	default:
		notFound("NotFound")
	}
}

// Registered reports whether namespace was registered.
func (f *Fake) Registered(namespace string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registered[strings.ToLower(namespace)]
}

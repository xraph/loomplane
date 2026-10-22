// Package azuretest is a fake of the parts of Microsoft Entra ID and
// Azure Resource Manager that Loomplane uses, for tests: one tenant, one
// subscription, and the resource group's registry, Container Apps
// environments, container apps, Postgres and MySQL flexible servers and
// Azure Managed Redis. Everything it makes reports provisioning once
// before it succeeds, so pollers are exercised.
//
//nolint:errcheck,gosec // A test fake: its secret is fake, and a request it can't read fails the test that sent it.
package azuretest

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
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
	// PullPrincipal is the principal ID of the pull identity.
	PullPrincipal = "66666666-6666-6666-6666-666666666666"
	// RedisKey is every Managed Redis database's primary access key. Like
	// Azure's, it has characters an address must escape.
	RedisKey = "fake/redis+key="
)

// RoleAssignment is a role Loomplane assigned.
type RoleAssignment struct {
	Scope, Name, RoleDefinitionID, PrincipalID, PrincipalType string
}

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
	FailBuild bool
	// PrincipalMissingFor makes the first N role assignments fail with
	// PrincipalNotFound, as they do while Entra ID hasn't replicated a new
	// identity yet.
	PrincipalMissingFor int
	// PullDeniedFor makes the first N container app revisions made after
	// a role assignment fail to pull their image, as they do while the
	// assignment propagates.
	PullDeniedFor   int
	principalMissed int
	pullDenied      map[string]bool   // lower-cased revision name
	groups          map[string]string // name → location
	registered      map[string]bool
	resources       map[string]map[string]any // lower-cased resource ID → body
	polls           map[string]int
	// Uploads are the source tarballs, by relative path.
	Uploads map[string][]byte
	// Tasks are the ACR Tasks files of the runs started, decoded.
	Tasks []string
	// Apps are the container app bodies Loomplane PUT, by name, and
	// AppPuts every one of them in order.
	Apps    map[string]map[string]any
	AppPuts []map[string]any
	Deleted []string
	// RoleAssignments are the roles assigned, each once.
	RoleAssignments []RoleAssignment
	// IdentityPuts counts the requests that made a managed identity.
	IdentityPuts int
	// ServerPuts are the flexible server bodies Loomplane PUT, in order,
	// passwords included. FirewallRules are the rules PUT, by server and
	// rule name, as "server/rule".
	ServerPuts    []map[string]any
	FirewallRules map[string]map[string]any
	// ServerState is the state a flexible server settles in once it's
	// done provisioning: Ready unless it's set.
	ServerState string
	// RedisPuts are the Managed Redis cluster bodies Loomplane PUT, and
	// RedisDatabasePuts its databases' bodies, in order.
	RedisPuts, RedisDatabasePuts []map[string]any
	// RedisUnavailableIn is a region that refuses a Managed Redis cluster,
	// with RedisUnavailableCode: SkuNotAvailable unless it's set.
	RedisUnavailableIn, RedisUnavailableCode string
}

// New starts a fake.
func New(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{
		Actions: []string{"*"}, NotActions: []string{"Microsoft.Authorization/*/Write", "Microsoft.Authorization/*/Delete"},
		groups: map[string]string{}, registered: map[string]bool{}, resources: map[string]map[string]any{},
		polls: map[string]int{}, Uploads: map[string][]byte{}, Apps: map[string]map[string]any{},
		pullDenied: map[string]bool{}, FirewallRules: map[string]map[string]any{},
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
	getAt := func(id string) {
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
			if latest, _ := props["latestRevisionName"].(string); kind == "microsoft.app/containerapps" && !f.pullDenied[strings.ToLower(latest)] {
				props["latestReadyRevisionName"] = latest
			}
		}
		if isServer(kind) {
			if f.polls[id] == 0 {
				props["state"] = "Provisioning"
			} else if props["state"] == "Provisioning" {
				props["state"] = cmp.Or(f.ServerState, "Ready")
			}
		}
		f.polls[id]++
		reply(http.StatusOK, res)
	}
	get := func() { getAt(id) }
	// An identity is there as soon as it's made.
	if kind == "microsoft.managedidentity/userassignedidentities" {
		f.identity(r, reply, notFound, id, canonical, name, body)
		return
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
		case "microsoft.dbforpostgresql/flexibleservers", "microsoft.dbformysql/flexibleservers":
			login, _ := props["administratorLogin"].(string)
			password, _ := props["administratorLoginPassword"].(string)
			if hasRunOf(password, login) {
				reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "PasswordNotComplex",
					"message": "The password must not contain all or part of the login name."}})
				return
			}
			f.ServerPuts = append(f.ServerPuts, record(body))
			// Azure never shows the password again.
			delete(props, "administratorLoginPassword")
			engine := map[bool]string{true: "postgres", false: "mysql"}[kind == "microsoft.dbforpostgresql/flexibleservers"]
			props["fullyQualifiedDomainName"] = strings.ToLower(name) + "." + engine + ".database.azure.com"
		case "microsoft.cache/redisenterprise":
			location, _ := body["location"].(string)
			if strings.EqualFold(location, f.RedisUnavailableIn) {
				reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": cmp.Or(f.RedisUnavailableCode, "SkuNotAvailable"),
					"message": "Managed Redis Balanced_B0 is not available in location " + location + "."}})
				return
			}
			f.RedisPuts = append(f.RedisPuts, record(body))
			props["hostName"] = strings.ToLower(name) + "." + strings.ToLower(location) + ".redis.azure.net"
		case "microsoft.app/containerapps":
			f.Apps[name] = body
			f.AppPuts = append(f.AppPuts, body)
			// Like Container Apps, a PUT makes a new revision only when the
			// template changed, its revisionSuffix included.
			if old := f.resources[id]; old != nil && reflect.DeepEqual(old["properties"].(map[string]any)["template"], props["template"]) {
				body["revision"] = old["revision"]
				props["latestRevisionName"] = old["properties"].(map[string]any)["latestRevisionName"]
			} else {
				f.newRevision(name, old, body, props)
			}
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
		for child := range f.resources {
			if strings.HasPrefix(child, id+"/") {
				delete(f.resources, child)
			}
		}
		f.Deleted = append(f.Deleted, name)
		for rule := range f.FirewallRules {
			if strings.HasPrefix(rule, name+"/") {
				delete(f.FirewallRules, rule)
			}
		}
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
	case kind == "microsoft.containerregistry/registries" && len(rest) == 4 && rest[0] == "providers" && rest[1] == "microsoft.authorization" && rest[2] == "roleassignments" && r.Method == http.MethodPut:
		if f.resources[id] == nil {
			notFound("ParentResourceNotFound")
			return
		}
		if f.principalMissed < f.PrincipalMissingFor {
			f.principalMissed++
			reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "PrincipalNotFound", "message": "Principal " + PullPrincipal + " does not exist in the directory " + Tenant + "."}})
			return
		}
		key := strings.ToLower(canonical) + "/" + rest[3]
		for _, have := range f.RoleAssignments {
			if strings.ToLower(have.Scope)+"/"+strings.ToLower(have.Name) == key {
				reply(http.StatusConflict, map[string]any{"error": map[string]string{"code": "RoleAssignmentExists", "message": "The role assignment already exists."}})
				return
			}
		}
		props, _ := body["properties"].(map[string]any)
		str := func(k string) string { v, _ := props[k].(string); return v }
		f.RoleAssignments = append(f.RoleAssignments, RoleAssignment{
			Scope: canonical, Name: strings.Split(strings.Trim(p, "/"), "/")[11],
			RoleDefinitionID: str("roleDefinitionId"), PrincipalID: str("principalId"), PrincipalType: str("principalType"),
		})
		reply(http.StatusCreated, map[string]any{"properties": props})
	case isServer(kind) && len(rest) == 2 && rest[0] == "firewallrules" && r.Method == http.MethodPut:
		if f.resources[id] == nil {
			notFound("ParentResourceNotFound")
			return
		}
		f.FirewallRules[name+"/"+strings.Split(strings.Trim(p, "/"), "/")[9]] = body
		reply(http.StatusAccepted, body)
	case kind == "microsoft.cache/redisenterprise" && len(rest) >= 2 && rest[0] == "databases":
		f.redisDatabase(r, reply, notFound, getAt, id, canonical, rest, body)
	case kind == "microsoft.app/containerapps" && len(rest) == 2 && rest[0] == "revisions" && f.pullDenied[rest[1]]:
		reply(http.StatusOK, map[string]any{"properties": map[string]any{"provisioningState": "Provisioned", "runningState": "Failed",
			"runningStateDetails": "Failed to pull image: UNAUTHORIZED: authentication required"}})
	case kind == "microsoft.app/containerapps" && len(rest) == 2 && rest[0] == "revisions":
		reply(http.StatusOK, map[string]any{"properties": map[string]any{"provisioningState": "Provisioning", "runningState": "Processing"}})
	default:
		notFound("NotFound")
	}
}

// redisDatabase serves a Managed Redis cluster's database and its keys.
func (f *Fake) redisDatabase(r *http.Request, reply func(int, any), notFound, getAt func(string), id, canonical string, rest []string, body map[string]any) {
	key := id + "/databases/" + rest[1]
	switch {
	case f.resources[id] == nil:
		notFound("ParentResourceNotFound")
	case len(rest) == 2 && r.Method == http.MethodGet:
		getAt(key)
	case len(rest) == 2 && r.Method == http.MethodPut:
		f.RedisDatabasePuts = append(f.RedisDatabasePuts, record(body))
		props, _ := body["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		body["id"], body["name"], body["properties"] = canonical+"/databases/"+rest[1], rest[1], props
		props["provisioningState"] = "InProgress"
		f.resources[key], f.polls[key] = body, 0
		reply(http.StatusCreated, body)
	case len(rest) == 3 && rest[2] == "listkeys" && r.Method == http.MethodPost:
		if f.resources[key] == nil {
			notFound("ResourceNotFound")
			return
		}
		reply(http.StatusOK, map[string]string{"primaryKey": RedisKey, "secondaryKey": "fake-secondary-key"})
	default:
		notFound("NotFound")
	}
}

// record is a deep copy of a request body, so what the fake changes in
// it later doesn't change what was sent.
func record(body map[string]any) map[string]any {
	var out map[string]any
	data, _ := json.Marshal(body)
	_ = json.Unmarshal(data, &out)
	return out
}

// hasRunOf reports whether password has three characters in a row of
// login, in either case, as Azure refuses.
func hasRunOf(password, login string) bool {
	password, login = strings.ToLower(password), strings.ToLower(login)
	for i := 0; i+3 <= len(login); i++ {
		if strings.Contains(password, login[i:i+3]) {
			return true
		}
	}
	return false
}

// isServer reports whether kind is a Postgres or MySQL flexible server.
func isServer(kind string) bool {
	return kind == "microsoft.dbforpostgresql/flexibleservers" || kind == "microsoft.dbformysql/flexibleservers"
}

// newRevision numbers a container app's new revision, or names it with
// the template's revisionSuffix, and denies it a pull while
// PullDeniedFor says so.
func (f *Fake) newRevision(name string, old, body, props map[string]any) {
	rev := 1
	if old != nil {
		rev = int(old["revision"].(float64)) + 1
	}
	body["revision"] = float64(rev)
	revision := name + "--" + strconv.Itoa(rev)
	if tmpl, _ := props["template"].(map[string]any); tmpl != nil {
		if suffix, _ := tmpl["revisionSuffix"].(string); suffix != "" {
			revision = name + "--" + suffix
		}
	}
	props["latestRevisionName"] = revision
	if len(f.RoleAssignments) > 0 && len(f.pullDenied) < f.PullDeniedFor {
		f.pullDenied[strings.ToLower(revision)] = true
	}
}

// identity serves a user-assigned managed identity.
func (f *Fake) identity(r *http.Request, reply func(int, any), notFound func(string), id, canonical, name string, body map[string]any) {
	switch r.Method {
	case http.MethodGet:
		if res := f.resources[id]; res != nil {
			reply(http.StatusOK, res)
			return
		}
		notFound("ResourceNotFound")
	case http.MethodPut:
		f.IdentityPuts++
		body["id"], body["name"] = canonical, name
		body["properties"] = map[string]any{"principalId": PullPrincipal, "clientId": "77777777-7777-7777-7777-777777777777"}
		f.resources[id] = body
		reply(http.StatusCreated, body)
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

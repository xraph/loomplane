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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
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
	// NoDigest makes a finished run name no image it pushed, as a run
	// whose push step reported nothing would.
	NoDigest bool
	// PrincipalMissingFor makes the first N role assignments fail with
	// PrincipalNotFound, as they do while Entra ID hasn't replicated a new
	// identity yet.
	PrincipalMissingFor int
	// PullDeniedFor makes the first N container app revisions made after
	// a role assignment fail to pull their image, as they do while the
	// assignment propagates.
	PullDeniedFor int
	// AppPullFailedFor makes the first N container app PUTs settle with
	// the app's own provisioningState Failed, and its revision's
	// provisioningError saying the managed identity couldn't pull, as
	// Container Apps reports a pull it gives up on while provisioning.
	AppPullFailedFor int
	// PutPullRefusedFor makes the first N container app PUTs answer 400,
	// saying the managed identity couldn't pull the image.
	PutPullRefusedFor int
	principalMissed   int
	appPuts           int
	pullDenied        map[string]bool   // lower-cased revision name
	provisionFailed   map[string]bool   // lower-cased revision name
	groups            map[string]string // name → location
	registered        map[string]bool
	resources         map[string]map[string]any // lower-cased resource ID → body
	polls             map[string]int
	// Uploads are the source tarballs, by relative path.
	Uploads map[string][]byte
	// Tasks are the ACR Tasks files of the runs started, decoded.
	Tasks []string
	// runTasks are those files by run ID.
	runTasks map[string]string
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
	// NotFoundAfterPut makes each resource Loomplane makes answer 404 to
	// its first N GETs, as Azure can while it's still making one.
	NotFoundAfterPut int
	unseen           map[string]int // lower-cased resource ID → 404s left
	// ServerState is the state a flexible server settles in once it's
	// done provisioning: Ready unless it's set.
	ServerState string
	// RedisPuts are the Managed Redis cluster bodies Loomplane PUT, and
	// RedisDatabasePuts its databases' bodies, in order.
	RedisPuts, RedisDatabasePuts []map[string]any
	// RedisUnavailableIn is a region that refuses a Managed Redis cluster,
	// with RedisUnavailableCode: SkuNotAvailable unless it's set.
	RedisUnavailableIn, RedisUnavailableCode string

	// FailImage makes every container app revision that runs this image
	// crash once it starts: its running state is Failed, and so is the
	// app's provisioning while it's the latest revision, as Container
	// Apps reports an app whose newest revision won't run.
	FailImage string
	// AllowDroppingInUseSecrets lets a PUT or PATCH drop a secret an active
	// revision reads. By default the fake refuses one, the safe model of
	// what Azure does, which isn't documented.
	AllowDroppingInUseSecrets bool
	// RefuseLatestDeactivation refuses to deactivate an app's latest
	// revision. Whether Container Apps does isn't documented, so a test
	// can check Loomplane copes either way.
	RefuseLatestDeactivation bool
	// AppPatches are the container app bodies Loomplane PATCHed, in
	// order, and Deactivations the revisions it deactivated.
	AppPatches    []map[string]any
	Deactivations []string
	// Calls are the container app calls Loomplane made, in order, by
	// kind ("PUT app", "PATCH app", "GET revision", "POST deactivate",
	// "POST listsecrets" and so on) and the app's name.
	Calls     []string
	fail      map[string]int
	failAt    map[string]int
	counts    map[string]int
	revisions map[string]*fakeRevision // lower-cased app ID + "/" + revision name
	crashed   map[string]bool          // lower-cased revision name
	history   map[string][]map[string]int
}

// fakeRevision is a container app revision: the template it was made
// from, and whether it's active and ready.
type fakeRevision struct {
	name     string
	template any
	active   bool
	ready    bool
	order    int
}

// New starts a fake.
func New(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{
		Actions: []string{"*"}, NotActions: []string{"Microsoft.Authorization/*/Write", "Microsoft.Authorization/*/Delete"},
		groups: map[string]string{}, registered: map[string]bool{}, resources: map[string]map[string]any{},
		polls: map[string]int{}, Uploads: map[string][]byte{}, Apps: map[string]map[string]any{}, runTasks: map[string]string{},
		pullDenied: map[string]bool{}, provisionFailed: map[string]bool{}, unseen: map[string]int{}, FirewallRules: map[string]map[string]any{},
		fail: map[string]int{}, failAt: map[string]int{}, counts: map[string]int{}, revisions: map[string]*fakeRevision{}, crashed: map[string]bool{},
		history: map[string][]map[string]int{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// Digest is the digest the fake reports for the image it pushed as ref,
// a repository and tag.
func Digest(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// pushed are the images a task file's push steps name, as ACR Tasks
// reports them on the run.
func pushed(server, task string) []map[string]string {
	var out []map[string]string
	for _, line := range strings.Split(task, "\n") {
		ref, ok := strings.CutPrefix(strings.TrimSpace(line), `- "{{.Run.Registry}}/`)
		if !ok {
			continue
		}
		ref = strings.TrimSuffix(ref, `"`)
		repo, tag, _ := strings.Cut(ref, ":")
		out = append(out, map[string]string{"registry": server, "repository": repo, "tag": tag, "digest": Digest(ref)})
	}
	return out
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
	if kind == "microsoft.app/containerapps" {
		// Whatever the call did to who serves is kept, for a test to read
		// what happened in between.
		defer f.recordShares(id)
		call := appCall(r.Method, rest)
		f.Calls = append(f.Calls, call+" "+name)
		f.counts[call]++
		// Like Resource Manager, each call needs its action, which Actions
		// and NotActions decide.
		if action := appActions[call]; action != "" && !f.allows(action) {
			reply(http.StatusForbidden, map[string]any{"error": map[string]string{"code": "AuthorizationFailed",
				"message": "The client '" + Client + "' does not have authorization to perform action '" + action + "'."}})
			return
		}
		if f.fail[call] > 0 || f.failAt[call] == f.counts[call] {
			if f.fail[call] > 0 {
				f.fail[call]--
			}
			reply(http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "ServiceUnavailable", "message": "the fake failed " + call}})
			return
		}
	}
	// GET reports provisioning once, then the final state.
	getAt := func(id string) {
		res := f.resources[id]
		if res == nil || f.unseen[id] > 0 {
			if res != nil {
				f.unseen[id]--
			}
			notFound("ResourceNotFound")
			return
		}
		props := res["properties"].(map[string]any)
		if f.polls[id] == 0 {
			props["provisioningState"] = "InProgress"
		} else if props["provisioningState"] == "InProgress" {
			props["provisioningState"] = "Succeeded"
			latest, _ := props["latestRevisionName"].(string)
			switch {
			case kind != "microsoft.app/containerapps" || f.pullDenied[strings.ToLower(latest)]:
			case f.provisionFailed[strings.ToLower(latest)] || f.crashed[strings.ToLower(latest)]:
				props["provisioningState"] = "Failed"
			default:
				props["latestReadyRevisionName"] = latest
				f.ready(id, res, latest)
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
		if kind == "microsoft.app/containerapps" {
			reply(http.StatusOK, withoutSecretValues(res))
			return
		}
		reply(http.StatusOK, res)
	}
	get := func() { getAt(id) }
	// Like Azure, a resource can't be made under a namespace the
	// subscription hasn't registered.
	if r.Method == http.MethodPut && (len(rest) == 0 || kind == "microsoft.managedidentity/userassignedidentities") && !f.registered[parts[5]] {
		reply(http.StatusConflict, map[string]any{"error": map[string]string{"code": "MissingSubscriptionRegistration",
			"message": "The subscription is not registered to use namespace '" + strings.Split(strings.Trim(p, "/"), "/")[5] + "'."}})
		return
	}
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
			f.AppPuts = append(f.AppPuts, body)
			f.appPuts++
			if f.appPuts <= f.PutPullRefusedFor {
				reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "ContainerAppOperationError",
					"message": "Failed to provision revision for container app '" + name + "'. Error details: Field 'template.containers.main.image' is invalid with details: " +
						"'Invalid value: unable to pull image using Managed identity " + PullPrincipal + "'."}})
				return
			}
			old := f.resources[id]
			// A PUT that sends no traffic keeps the split there was. Azure
			// may route it all to the latest revision instead, but nothing
			// here relies on that.
			if ing := ingressOf(body); ing != nil && ing["traffic"] == nil && old != nil {
				if was := ingressOf(old); was != nil && was["traffic"] != nil {
					ing["traffic"] = record(map[string]any{"t": was["traffic"]})["t"]
				}
			}
			if refused := f.change(id, name, old, body); refused != "" {
				reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "ContainerAppOperationError", "message": refused}})
				return
			}
			f.Apps[name] = body
			if conf, _ := props["configuration"].(map[string]any); conf != nil {
				if ing, _ := conf["ingress"].(map[string]any); ing != nil {
					ing["fqdn"] = strings.TrimPrefix(f.Server.URL, "http://") + "/apps/" + name
				}
			}
		}
		if f.resources[id] == nil {
			f.unseen[id] = f.NotFoundAfterPut
		}
		f.resources[id], f.polls[id] = body, 0
		if kind == "microsoft.app/containerapps" {
			reply(http.StatusCreated, withoutSecretValues(body))
			return
		}
		reply(http.StatusCreated, body)
	case kind == "microsoft.app/containerapps" && len(rest) == 0 && r.Method == http.MethodPatch:
		f.patchApp(reply, notFound, id, name, body)
	case kind == "microsoft.app/containerapps" && len(rest) == 1 && rest[0] == "listsecrets" && r.Method == http.MethodPost:
		app := f.resources[id]
		if app == nil {
			notFound("ResourceNotFound")
			return
		}
		secrets, _ := configurationOf(app)["secrets"].([]any)
		if secrets == nil {
			secrets = []any{}
		}
		reply(http.StatusOK, map[string]any{"value": record(map[string]any{"s": secrets})["s"]})
	case kind == "microsoft.app/containerapps" && len(rest) >= 1 && rest[0] == "revisions":
		f.revision(r, reply, notFound, id, rest)
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
		f.runTasks[run] = string(task)
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
		props := map[string]any{"runId": rest[1], "status": status, "runErrorMessage": map[bool]string{true: "The command returned a non-zero code: 1"}[status == "Failed"]}
		if status == "Succeeded" && !f.NoDigest {
			props["outputImages"] = pushed(name+".azurecr.io", f.runTasks[rest[1]])
		}
		reply(http.StatusOK, map[string]any{"properties": props})
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
		if f.resources[key] == nil {
			f.unseen[key] = f.NotFoundAfterPut
		}
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

// change decides what a PUT or PATCH of a container app makes of it: the
// new body replaces old. Like Container Apps, it makes a new revision only
// when the template changed, its revisionSuffix included, and refuses a
// suffix a revision already has. It refuses traffic that doesn't add up to
// 100, that names a revision that isn't there, isn't active or, for a
// share, isn't ready, and in single revision mode any traffic that names a
// revision. It returns why it refused, or "".
func (f *Fake) change(id, name string, old, body map[string]any) string {
	props, _ := body["properties"].(map[string]any)
	var was map[string]any
	if old != nil {
		was, _ = old["properties"].(map[string]any)
		// Read only, so a PUT never sends it.
		if ready, _ := was["latestReadyRevisionName"].(string); ready != "" {
			props["latestReadyRevisionName"] = ready
		}
	}
	created := ""
	if was != nil && reflect.DeepEqual(was["template"], props["template"]) {
		body["revision"] = old["revision"]
		props["latestRevisionName"] = was["latestRevisionName"]
	} else {
		rev := 1
		if old != nil {
			rev = int(old["revision"].(float64)) + 1
		}
		created = name + "--" + strconv.Itoa(rev)
		if tmpl, _ := props["template"].(map[string]any); tmpl != nil {
			if suffix, _ := tmpl["revisionSuffix"].(string); suffix != "" {
				created = name + "--" + suffix
			}
		}
		created = strings.ToLower(created)
		if f.revisions[id+"/"+created] != nil {
			return "Revision " + created + " already exists. Use a revision suffix the app hasn't used."
		}
		body["revision"] = float64(rev)
		props["latestRevisionName"] = created
	}
	if refused := f.checkTraffic(id, props); refused != "" {
		return refused
	}
	if refused := f.checkSecrets(id, props); refused != "" {
		return refused
	}
	if created != "" {
		f.newRevision(id, created, props)
	}
	return ""
}

// checkTraffic is why Container Apps would refuse an app's traffic, or "".
func (f *Fake) checkTraffic(id string, props map[string]any) string {
	conf, _ := props["configuration"].(map[string]any)
	mode, _ := conf["activeRevisionsMode"].(string)
	ing, _ := conf["ingress"].(map[string]any)
	traffic, _ := ing["traffic"].([]any)
	if len(traffic) == 0 {
		return ""
	}
	total := 0
	for _, e := range traffic {
		w, _ := e.(map[string]any)
		weight, _ := w["weight"].(float64)
		latest, _ := w["latestRevision"].(bool)
		named, _ := w["revisionName"].(string)
		total += int(weight)
		if named == "" {
			continue
		}
		if !strings.EqualFold(mode, "Multiple") && !latest {
			return "In single revision mode, all of the traffic goes to the latest revision; it can't name revision " + named + "."
		}
		rev := f.revisions[id+"/"+strings.ToLower(named)]
		switch {
		case rev == nil:
			return "Revision " + named + " not found."
		case !rev.active:
			return "Revision " + named + " isn't active, so it can't take traffic."
		case weight > 0 && (!rev.ready || f.crashed[rev.name]):
			return "Revision " + named + " isn't ready, so it can't take traffic."
		}
	}
	if total != 100 {
		return "The traffic weights add up to " + strconv.Itoa(total) + ", not 100."
	}
	return ""
}

// checkSecrets is why Container Apps would refuse an app's secrets, or "":
// a secret the template, or an active revision, reads that isn't there.
// Azure may let one go that an active revision reads instead, and that
// revision then can't start a replica; refusing is the safe model.
func (f *Fake) checkSecrets(id string, props map[string]any) string {
	conf, _ := props["configuration"].(map[string]any)
	secrets, _ := conf["secrets"].([]any)
	have := map[string]bool{}
	for _, sec := range secrets {
		name, _ := sec.(map[string]any)["name"].(string)
		have[name] = true
	}
	missing := func(tmpl any, who string) string {
		for _, ref := range secretRefs(tmpl) {
			if !have[ref] {
				return "Secret " + ref + ", which " + who + " reads, isn't among the app's secrets."
			}
		}
		return ""
	}
	if refused := missing(props["template"], "the template"); refused != "" {
		return refused
	}
	if f.AllowDroppingInUseSecrets {
		return ""
	}
	for _, key := range slices.Sorted(maps.Keys(f.revisions)) {
		if rev := f.revisions[key]; strings.HasPrefix(key, id+"/") && rev.active {
			if refused := missing(rev.template, "active revision "+rev.name); refused != "" {
				return refused
			}
		}
	}
	return ""
}

// secretRefs are the secrets a template's containers read.
func secretRefs(tmpl any) []string {
	var out []string
	t, _ := tmpl.(map[string]any)
	cs, _ := t["containers"].([]any)
	for _, c := range cs {
		env, _ := c.(map[string]any)["env"].([]any)
		for _, e := range env {
			if ref, _ := e.(map[string]any)["secretRef"].(string); ref != "" {
				out = append(out, ref)
			}
		}
	}
	return out
}

// newRevision records a container app's new revision, active and not
// ready yet, and denies it a pull while PullDeniedFor or AppPullFailedFor
// says so, or crashes it when it runs FailImage.
func (f *Fake) newRevision(id, revision string, props map[string]any) {
	tmpl := record(map[string]any{"t": props["template"]})["t"]
	f.revisions[id+"/"+revision] = &fakeRevision{name: revision, template: tmpl, active: true, order: len(f.revisions)}
	if len(f.RoleAssignments) > 0 && len(f.pullDenied) < f.PullDeniedFor {
		f.pullDenied[revision] = true
	}
	if f.appPuts-f.PutPullRefusedFor <= f.AppPullFailedFor {
		f.provisionFailed[revision] = true
	}
	if t, _ := tmpl.(map[string]any); f.FailImage != "" && t != nil {
		if cs, _ := t["containers"].([]any); len(cs) > 0 && cs[0].(map[string]any)["image"] == f.FailImage {
			f.crashed[revision] = true
		}
	}
}

// ready marks an app's latest revision ready. In single revision mode
// that deactivates every other revision, as Container Apps does once the
// new one can take over.
func (f *Fake) ready(id string, app map[string]any, latest string) {
	rev := f.revisions[id+"/"+strings.ToLower(latest)]
	if rev == nil {
		return
	}
	rev.ready = true
	if mode, _ := configurationOf(app)["activeRevisionsMode"].(string); strings.EqualFold(mode, "Multiple") {
		return
	}
	for key, other := range f.revisions {
		if strings.HasPrefix(key, id+"/") && other != rev {
			other.active = false
		}
	}
}

// patchApp merges a PATCH into a container app, as JSON Merge Patch does.
func (f *Fake) patchApp(reply func(int, any), notFound func(string), id, name string, body map[string]any) {
	old := f.resources[id]
	if old == nil {
		notFound("ResourceNotFound")
		return
	}
	f.AppPatches = append(f.AppPatches, record(body))
	merged := record(old)
	mergePatch(merged, body)
	if refused := f.change(id, name, old, merged); refused != "" {
		reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "ContainerAppOperationError", "message": refused}})
		return
	}
	merged["properties"].(map[string]any)["provisioningState"] = "InProgress"
	f.resources[id], f.polls[id] = merged, 0
	reply(http.StatusOK, withoutSecretValues(merged))
}

// mergePatch applies patch to dst as RFC 7386 says: objects merge, null
// removes, and anything else, arrays included, replaces.
func mergePatch(dst, patch map[string]any) {
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(dst, k)
		case map[string]any:
			dv, ok := dst[k].(map[string]any)
			if !ok {
				dv = map[string]any{}
				dst[k] = dv
			}
			mergePatch(dv, pv)
		default:
			dst[k] = v
		}
	}
}

// revision serves a container app's revisions: listing them, reading one,
// and deactivating or activating one.
func (f *Fake) revision(r *http.Request, reply func(int, any), notFound func(string), id string, rest []string) {
	if f.resources[id] == nil {
		notFound("ResourceNotFound")
		return
	}
	var rev *fakeRevision
	if len(rest) >= 2 {
		rev = f.revisions[id+"/"+rest[1]]
	}
	switch {
	case len(rest) == 1 && r.Method == http.MethodGet:
		var all []*fakeRevision
		for key, rev := range f.revisions {
			if strings.HasPrefix(key, id+"/") {
				all = append(all, rev)
			}
		}
		slices.SortFunc(all, func(a, b *fakeRevision) int { return a.order - b.order })
		value := make([]any, 0, len(all))
		for _, rev := range all {
			value = append(value, f.revisionView(id, rev))
		}
		reply(http.StatusOK, map[string]any{"value": value})
	case len(rest) == 2 && r.Method == http.MethodGet && f.provisionFailed[rest[1]]:
		reply(http.StatusOK, map[string]any{"properties": map[string]any{"provisioningState": "Failed", "runningState": "Failed",
			"provisioningError": "Failed to provision revision for container app. Error details: unable to pull image using Managed identity " + PullPrincipal + "."}})
	case len(rest) == 2 && r.Method == http.MethodGet && f.pullDenied[rest[1]]:
		reply(http.StatusOK, map[string]any{"properties": map[string]any{"provisioningState": "Provisioned", "runningState": "Failed",
			"runningStateDetails": "Failed to pull image: UNAUTHORIZED: authentication required"}})
	case rev == nil:
		notFound("ResourceNotFound")
	case len(rest) == 2 && r.Method == http.MethodGet:
		reply(http.StatusOK, f.revisionView(id, rev))
	case len(rest) == 3 && rest[2] == "deactivate" && r.Method == http.MethodPost:
		latest, _ := f.resources[id]["properties"].(map[string]any)["latestRevisionName"].(string)
		switch {
		case f.shares(id)[rev.name] > 0:
			reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "ContainerAppOperationError", "message": "Revision " + rev.name + " takes traffic, so it can't be deactivated."}})
			return
		case f.RefuseLatestDeactivation && strings.EqualFold(latest, rev.name):
			reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "ContainerAppOperationError", "message": "Revision " + rev.name + " is the latest revision, so it can't be deactivated."}})
			return
		}
		rev.active = false
		f.Deactivations = append(f.Deactivations, rev.name)
		reply(http.StatusOK, map[string]any{})
	case len(rest) == 3 && rest[2] == "activate" && r.Method == http.MethodPost:
		rev.active = true
		reply(http.StatusOK, map[string]any{})
	default:
		notFound("NotFound")
	}
}

// revisionView is a revision as Azure shows it.
func (f *Fake) revisionView(id string, rev *fakeRevision) map[string]any {
	props := map[string]any{"active": rev.active, "template": rev.template, "trafficWeight": f.shares(id)[rev.name]}
	state := func(provisioning, running, health string) {
		props["provisioningState"], props["runningState"], props["healthState"] = provisioning, running, health
	}
	switch {
	case f.crashed[rev.name]:
		state("Provisioned", "Failed", "Unhealthy")
		props["runningStateDetails"] = "The container exited with status 1."
	case !rev.ready:
		state("Provisioning", "Processing", "None")
	case !rev.active:
		state("Provisioned", "Stopped", "None")
	default:
		state("Provisioned", "Running", "Healthy")
	}
	return map[string]any{"id": id + "/revisions/" + rev.name, "name": rev.name, "properties": props}
}

// shares is each revision's percent of an app's traffic now: the latest
// ready revision's in single revision mode, else as its traffic says.
func (f *Fake) shares(id string) map[string]int {
	out := map[string]int{}
	app := f.resources[id]
	if app == nil {
		return out
	}
	conf := configurationOf(app)
	ready, _ := app["properties"].(map[string]any)["latestReadyRevisionName"].(string)
	ready = strings.ToLower(ready)
	mode, _ := conf["activeRevisionsMode"].(string)
	traffic, _ := ingressOf(app)["traffic"].([]any)
	if !strings.EqualFold(mode, "Multiple") {
		// One revision serves in single revision mode, and only one that's
		// active and ready: the latest ready, or else the newest that is.
		if serving := f.singleServing(id, ready); serving != "" {
			out[serving] = 100
		}
		return out
	}
	if len(traffic) == 0 {
		if ready != "" {
			out[ready] = 100
		}
		return out
	}
	for _, e := range traffic {
		w, _ := e.(map[string]any)
		weight, _ := w["weight"].(float64)
		named, _ := w["revisionName"].(string)
		if latest, _ := w["latestRevision"].(bool); latest || named == "" {
			named = ready
		}
		out[strings.ToLower(named)] += int(weight)
	}
	return out
}

// singleServing is the revision that serves an app in single revision
// mode: the latest ready one while it's active and running, else the
// newest one that is, else none.
func (f *Fake) singleServing(id, latestReady string) string {
	up := func(rev *fakeRevision) bool { return rev != nil && rev.active && rev.ready && !f.crashed[rev.name] }
	if rev := f.revisions[id+"/"+latestReady]; up(rev) {
		return rev.name
	}
	var best *fakeRevision
	for key, rev := range f.revisions {
		if strings.HasPrefix(key, id+"/") && up(rev) && (best == nil || rev.order > best.order) {
			best = rev
		}
	}
	if best == nil {
		return ""
	}
	return best.name
}

// recordShares keeps who serves an app now, when that changed.
func (f *Fake) recordShares(id string) {
	now := f.shares(id)
	maps.DeleteFunc(now, func(_ string, v int) bool { return v == 0 })
	if h := f.history[id]; len(h) == 0 || !maps.Equal(h[len(h)-1], now) {
		f.history[id] = append(h, now)
	}
}

// configurationOf is a container app's configuration, or an empty one.
func configurationOf(app map[string]any) map[string]any {
	props, _ := app["properties"].(map[string]any)
	conf, _ := props["configuration"].(map[string]any)
	if conf == nil {
		return map[string]any{}
	}
	return conf
}

// ingressOf is a container app's ingress, or nil.
func ingressOf(app map[string]any) map[string]any {
	ing, _ := configurationOf(app)["ingress"].(map[string]any)
	return ing
}

// withoutSecretValues is a container app as Azure shows it: its secrets'
// names without their values.
func withoutSecretValues(app map[string]any) map[string]any {
	out := record(app)
	secrets, _ := configurationOf(out)["secrets"].([]any)
	for _, s := range secrets {
		delete(s.(map[string]any), "value")
	}
	return out
}

// appActions are the actions each kind of container app call needs.
var appActions = map[string]string{
	"GET app":          "Microsoft.App/containerApps/read",
	"PUT app":          "Microsoft.App/containerApps/write",
	"PATCH app":        "Microsoft.App/containerApps/write",
	"DELETE app":       "Microsoft.App/containerApps/delete",
	"GET revisions":    "Microsoft.App/containerApps/revisions/read",
	"GET revision":     "Microsoft.App/containerApps/revisions/read",
	"POST deactivate":  "Microsoft.App/containerApps/revisions/deactivate/action",
	"POST activate":    "Microsoft.App/containerApps/revisions/activate/action",
	"POST listsecrets": "Microsoft.App/containerApps/listSecrets/action",
}

// allows reports whether Actions allow action and NotActions don't, with
// Azure's wildcard, where * is any run of characters.
func (f *Fake) allows(action string) bool {
	match := func(patterns []string) bool {
		for _, p := range patterns {
			re := "(?i)^" + strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*") + "$"
			if regexp.MustCompile(re).MatchString(action) {
				return true
			}
		}
		return false
	}
	return match(f.Actions) && !match(f.NotActions)
}

// appCall is the kind of a container app call, as Calls and FailNext name
// it: "PUT app", "GET revision", "POST deactivate" and so on.
func appCall(method string, rest []string) string {
	switch {
	case len(rest) == 0:
		return method + " app"
	case rest[0] == "revisions" && len(rest) == 1:
		return method + " revisions"
	case rest[0] == "revisions" && len(rest) == 2:
		return method + " revision"
	case rest[0] == "revisions":
		return method + " " + rest[2]
	}
	return method + " " + rest[0]
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

// --- What a test reads and changes of container apps ---

// FakeRevision is a container app revision as the fake keeps it.
type FakeRevision struct {
	Name          string
	Active, Ready bool
	// Image is what its first container runs.
	Image    string
	Template map[string]any
}

// appID is the fake's key for the container app called name.
func (f *Fake) appID(name string) string {
	suffix := "/providers/microsoft.app/containerapps/" + strings.ToLower(name)
	for id := range f.resources {
		if strings.HasSuffix(id, suffix) {
			return id
		}
	}
	return ""
}

// FailNext makes the next container app call of a kind, as Calls names
// it, answer 503.
func (f *Fake) FailNext(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[call] = 1
}

// FailAfter lets n more container app calls of a kind through, then
// fails one.
func (f *Fake) FailAfter(call string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt[call] = f.counts[call] + n + 1
}

// Split is how the traffic of the container app called name resolves
// now, by revision, without the zeros.
func (f *Fake) Split(name string) map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.shares(f.appID(name))
	maps.DeleteFunc(out, func(_ string, v int) bool { return v == 0 })
	return out
}

// App is the container app called name as the fake keeps it now, secret
// values included, or nil.
func (f *Fake) App(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	app := f.resources[f.appID(name)]
	if app == nil {
		return nil
	}
	return record(app)
}

// Mode is the active revisions mode of the container app called name.
func (f *Fake) Mode(name string) string {
	app := f.App(name)
	mode, _ := configurationOf(app)["activeRevisionsMode"].(string)
	return mode
}

// Secrets are the secrets of the container app called name, by name,
// with their values.
func (f *Fake) Secrets(name string) map[string]string {
	out := map[string]string{}
	secrets, _ := configurationOf(f.App(name))["secrets"].([]any)
	for _, s := range secrets {
		m := s.(map[string]any)
		k, _ := m["name"].(string)
		v, _ := m["value"].(string)
		out[k] = v
	}
	return out
}

// Revisions are the revisions of the container app called name, by name.
func (f *Fake) Revisions(name string) map[string]FakeRevision {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.appID(name)
	out := map[string]FakeRevision{}
	for key, rev := range f.revisions {
		if !strings.HasPrefix(key, id+"/") {
			continue
		}
		v := FakeRevision{Name: rev.name, Active: rev.active, Ready: rev.ready && !f.crashed[rev.name]}
		v.Template, _ = record(map[string]any{"t": rev.template})["t"].(map[string]any)
		if cs, _ := v.Template["containers"].([]any); len(cs) > 0 {
			v.Image, _ = cs[0].(map[string]any)["image"].(string)
		}
		out[rev.name] = v
	}
	return out
}

// ActiveRevisions are the names of the active revisions of the container
// app called name, sorted.
func (f *Fake) ActiveRevisions(name string) []string {
	var out []string
	for n, rev := range f.Revisions(name) {
		if rev.Active {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// DropRevision makes a revision of the container app called name gone,
// as if Azure had purged it.
func (f *Fake) DropRevision(name, revision string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.revisions, f.appID(name)+"/"+strings.ToLower(revision))
}

// Crash makes a revision of the container app called name crash, as a
// revision that started can later.
func (f *Fake) Crash(name, revision string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revisions[f.appID(name)+"/"+strings.ToLower(revision)] != nil {
		f.crashed[strings.ToLower(revision)] = true
	}
}

// TrafficHistory is every split the container app called name has had, in
// order, as each call left it, without the zeros.
func (f *Fake) TrafficHistory(name string) []map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.history[f.appID(name)]
	out := make([]map[string]int, len(h))
	for i, m := range h {
		out[i] = maps.Clone(m)
	}
	return out
}

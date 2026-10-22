package azure

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// API versions.
const (
	subscriptionsVersion = "2022-12-01"
	resourcesVersion     = "2021-04-01"
	permissionsVersion   = "2022-04-01"
)

// Actions are what a service principal needs for Loomplane to build a
// service with ACR Tasks and run it on Container Apps, and to make
// Postgres and MySQL flexible servers and Azure Managed Redis, in the
// resource group it deploys to. Each is checked before a connection counts as ready. The
// Contributor role on the resource group or the subscription has them
// all.
var Actions = []string{
	"Microsoft.Resources/subscriptions/resourceGroups/read",
	"Microsoft.ContainerRegistry/registries/read",
	"Microsoft.ContainerRegistry/registries/write",
	"Microsoft.ContainerRegistry/registries/listCredentials/action",
	"Microsoft.ContainerRegistry/registries/listBuildSourceUploadUrl/action",
	"Microsoft.ContainerRegistry/registries/scheduleRun/action",
	"Microsoft.ContainerRegistry/registries/runs/read",
	"Microsoft.App/managedEnvironments/read",
	"Microsoft.App/managedEnvironments/write",
	"Microsoft.App/managedEnvironments/join/action",
	"Microsoft.App/containerApps/read",
	"Microsoft.App/containerApps/write",
	"Microsoft.App/containerApps/delete",
	"Microsoft.DBforPostgreSQL/flexibleServers/read",
	"Microsoft.DBforPostgreSQL/flexibleServers/write",
	"Microsoft.DBforPostgreSQL/flexibleServers/delete",
	"Microsoft.DBforPostgreSQL/flexibleServers/firewallRules/write",
	"Microsoft.DBforMySQL/flexibleServers/read",
	"Microsoft.DBforMySQL/flexibleServers/write",
	"Microsoft.DBforMySQL/flexibleServers/delete",
	"Microsoft.DBforMySQL/flexibleServers/firewallRules/write",
	"Microsoft.Cache/redisEnterprise/read",
	"Microsoft.Cache/redisEnterprise/write",
	"Microsoft.Cache/redisEnterprise/delete",
	"Microsoft.Cache/redisEnterprise/databases/read",
	"Microsoft.Cache/redisEnterprise/databases/write",
	"Microsoft.Cache/redisEnterprise/databases/listKeys/action",
}

// createGroup is what making the resource group takes, needed only while
// it isn't there.
const createGroup = "Microsoft.Resources/subscriptions/resourceGroups/write"

// Namespaces are the resource providers a subscription must have
// registered. Loomplane registers one that isn't, which takes the
// namespace's register action. A database's namespace, Microsoft.Cache
// for Redis among them, isn't one of them: it's registered when a
// deployment first makes that database, so a subscription that only runs
// apps needn't have it.
var Namespaces = []string{"Microsoft.App", "Microsoft.ContainerRegistry"}

// Subscription is the subscription a principal deploys to.
type Subscription struct {
	ID          string `json:"subscriptionId"`
	TenantID    string `json:"tenantId"`
	DisplayName string `json:"displayName"`
	State       string `json:"state"`
}

// Subscription reads the session's subscription. Azure answers 404 for
// one the principal has no role in, which is a refusal.
func (s *Session) Subscription(ctx context.Context) (*Subscription, error) {
	var sub Subscription
	_, err := s.do(ctx, http.MethodGet, s.subscription(), subscriptionsVersion, nil, &sub)
	if IsNotFound(err) || isForbidden(err) {
		return nil, fmt.Errorf("%w: it has no role in subscription %s. Assign it Contributor on the subscription or on resource group %s", ErrRejected, s.cred.SubscriptionID, s.cred.ResourceGroup)
	}
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(sub.State) {
	case "disabled", "deleted", "expired", "pastdue":
		return nil, fmt.Errorf("%w: subscription %s is %s", ErrRejected, sub.DisplayName, strings.ToLower(sub.State))
	}
	return &sub, nil
}

func isForbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusForbidden
}

// permission is one entry of what a principal may do at a scope: what
// its role assignments there allow, less what they carve out.
type permission struct {
	Actions    []string `json:"actions"`
	NotActions []string `json:"notActions"`
}

// permissions lists what the principal may do at scope. A resource group
// that isn't there is IsNotFound.
func (s *Session) permissions(ctx context.Context, scope string) ([]permission, error) {
	var all []permission
	next := scope + "/providers/Microsoft.Authorization/permissions"
	version := permissionsVersion
	for next != "" {
		var page struct {
			Value    []permission `json:"value"`
			NextLink string       `json:"nextLink"`
		}
		if _, err := s.do(ctx, http.MethodGet, next, version, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		// The next link carries its own api-version.
		next, version = page.NextLink, ""
	}
	return all, nil
}

// allowed reports whether perms allow action: some entry's actions match
// it and the same entry's notActions don't.
func allowed(perms []permission, action string) bool {
	for _, p := range perms {
		if matchAny(p.Actions, action) && !matchAny(p.NotActions, action) {
			return true
		}
	}
	return false
}

func matchAny(patterns []string, action string) bool {
	for _, p := range patterns {
		if match(strings.ToLower(p), strings.ToLower(action)) {
			return true
		}
	}
	return false
}

// match is Azure's action wildcard: * stands for any run of characters,
// slashes included, as in Microsoft.App/* or */read.
func match(pattern, s string) bool {
	star, mark := -1, 0
	i, j := 0, 0
	for j < len(s) {
		switch {
		case i < len(pattern) && pattern[i] == '*':
			star, mark = i, j
			i++
		case i < len(pattern) && pattern[i] == s[j]:
			i++
			j++
		case star >= 0:
			i, mark = star+1, mark+1
			j = mark
		default:
			return false
		}
	}
	for i < len(pattern) && pattern[i] == '*' {
		i++
	}
	return i == len(pattern)
}

// registered reports whether namespace is registered in the
// subscription.
func (s *Session) registered(ctx context.Context, namespace string) (bool, error) {
	var out struct {
		RegistrationState string `json:"registrationState"`
	}
	if _, err := s.do(ctx, http.MethodGet, s.subscription()+"/providers/"+namespace, resourcesVersion, nil, &out); err != nil {
		return false, err
	}
	return strings.EqualFold(out.RegistrationState, "Registered"), nil
}

// groupExists reports whether the session's resource group is there.
func (s *Session) groupExists(ctx context.Context) (bool, error) {
	_, err := s.do(ctx, http.MethodGet, s.group(), resourcesVersion, nil, nil)
	switch {
	case IsNotFound(err):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// checkScope is where a deployment's permissions are read: the resource
// group, or the subscription while the group isn't there. A principal
// that may not even read the group is checked in the group, where it
// may do nothing.
func (s *Session) checkScope(ctx context.Context) (scope string, groupMissing bool, err error) {
	exists, err := s.groupExists(ctx)
	if isForbidden(err) {
		return s.group(), false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !exists {
		return s.subscription(), true, nil
	}
	return s.group(), false, nil
}

// permissionsOrNone is permissions, except that a principal without any
// role at scope may not read its own permissions there: it may do none
// of it.
func (s *Session) permissionsOrNone(ctx context.Context, scope string) ([]permission, error) {
	perms, err := s.permissions(ctx, scope)
	if isForbidden(err) {
		return nil, nil
	}
	return perms, err
}

// Missing returns which actions a deployment needs the principal lacks,
// in the order Actions lists them: Actions in the resource group, or in
// the subscription along with making the group while it isn't there,
// and registering each of Namespaces the subscription hasn't.
func (s *Session) Missing(ctx context.Context) ([]string, error) {
	scope, groupMissing, err := s.checkScope(ctx)
	if err != nil {
		return nil, err
	}
	want := Actions
	if groupMissing {
		want = append([]string{createGroup}, Actions...)
	}
	var subscriptionWide []string
	for _, ns := range Namespaces {
		ok, rerr := s.registered(ctx, ns)
		if rerr != nil {
			return nil, rerr
		}
		if !ok {
			subscriptionWide = append(subscriptionWide, ns+"/register/action")
		}
	}
	perms, err := s.permissionsOrNone(ctx, scope)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, action := range want {
		if !allowed(perms, action) {
			missing = append(missing, action)
		}
	}
	if len(subscriptionWide) > 0 {
		subPerms := perms
		if scope != s.subscription() {
			if subPerms, err = s.permissionsOrNone(ctx, s.subscription()); err != nil {
				return nil, err
			}
		}
		for _, action := range subscriptionWide {
			if !allowed(subPerms, action) {
				missing = append(missing, action)
			}
		}
	}
	return missing, nil
}

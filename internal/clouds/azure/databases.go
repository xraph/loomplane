package azure

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// API versions of the flexible servers.
const (
	postgresVersion = "2024-08-01"
	mysqlVersion    = "2023-12-30"
)

// AllowAzureServices is the firewall rule that lets in traffic from
// Azure's own addresses, which is how Container Apps reach a flexible
// server with public access. It admits every Azure tenant, not only
// yours; the server's TLS and password are what keep others out.
const AllowAzureServices = "AllowAllAzureServicesAndResourcesWithinAzureIps"

// flexibleEngine is how Azure offers an engine as a flexible server.
type flexibleEngine struct {
	namespace, version, title string
	storageGB                 int
}

// flexibleEngines are the flexible servers, by Loomplane's resource kind.
// Both run on the smallest burstable size.
var flexibleEngines = map[string]flexibleEngine{
	"postgres": {namespace: "Microsoft.DBforPostgreSQL", version: postgresVersion, title: "PostgreSQL", storageGB: 32},
	"mysql":    {namespace: "Microsoft.DBforMySQL", version: mysqlVersion, title: "MySQL", storageGB: 20},
}

// FlexibleServer is a Postgres or MySQL flexible server to make: Engine
// is "postgres" or "mysql", and Login and Password are its admin user.
type FlexibleServer struct {
	Engine, Name, Location, Version, Login, Password string
}

// Server is what Azure says of a flexible server.
type Server struct {
	ID         string `json:"id"`
	Properties struct {
		State   string `json:"state"`
		Version string `json:"version"`
		FQDN    string `json:"fullyQualifiedDomainName"`
	} `json:"properties"`
}

// FlexibleServerPath is the resource ID of the engine's flexible server
// called name in the session's resource group.
func (s *Session) FlexibleServerPath(engine, name string) string {
	return s.group() + "/providers/" + flexibleEngines[engine].namespace + "/flexibleServers/" + url.PathEscape(name)
}

// FlexibleServerEngine is the engine of the flexible server id names,
// "postgres" or "mysql", or "" when it names none.
func FlexibleServerEngine(id string) string {
	lower := strings.ToLower(id)
	for engine, e := range flexibleEngines {
		if strings.Contains(lower, "/providers/"+strings.ToLower(e.namespace)+"/flexibleservers/") {
			return engine
		}
	}
	return ""
}

// ownsServer returns the engine of the flexible server id names when
// it's one in the session's subscription, so nothing else is read or
// deleted through it.
func (s *Session) ownsServer(id string) (flexibleEngine, bool) {
	prefix := strings.ToLower(s.subscription() + "/resourceGroups/")
	engine := FlexibleServerEngine(id)
	if engine == "" || !strings.HasPrefix(strings.ToLower(id), prefix) || strings.Contains(id, "..") {
		return flexibleEngine{}, false
	}
	return flexibleEngines[engine], true
}

// EnsureFlexibleServer makes or updates the flexible server fs, with
// public access and Azure's services let in, and returns its host name
// once it's ready. The first one of an engine registers the engine's
// namespace in the subscription.
func (s *Session) EnsureFlexibleServer(ctx context.Context, fs FlexibleServer) (fqdn string, err error) {
	e, ok := flexibleEngines[fs.Engine]
	if !ok {
		return "", fmt.Errorf("azure has no flexible server for %s", fs.Engine)
	}
	if rerr := s.register(ctx, e.namespace); rerr != nil {
		return "", rerr
	}
	path := s.FlexibleServerPath(fs.Engine, fs.Name)
	body := map[string]any{
		"location": fs.Location,
		"tags":     map[string]string{"managed-by": "loomplane"},
		"sku":      map[string]string{"name": "Standard_B1ms", "tier": "Burstable"},
		"properties": map[string]any{
			"version":                    fs.Version,
			"administratorLogin":         fs.Login,
			"administratorLoginPassword": fs.Password,
			"storage":                    map[string]any{"storageSizeGB": e.storageGB},
			"network":                    map[string]any{"publicNetworkAccess": "Enabled"},
			"highAvailability":           map[string]any{"mode": "Disabled"},
		},
	}
	if _, perr := s.do(ctx, http.MethodPut, path, e.version, body, nil); perr != nil {
		return "", fmt.Errorf("make the %s flexible server %s: %w", e.title, fs.Name, perr)
	}
	var srv Server
	what := "the " + e.title + " flexible server " + fs.Name
	if werr := s.provisioned(ctx, path, e.version, what, &srv, func() string { return srv.Properties.State }); werr != nil {
		return "", werr
	}
	rule := map[string]any{"properties": map[string]string{"startIpAddress": "0.0.0.0", "endIpAddress": "0.0.0.0"}}
	if _, ferr := s.do(ctx, http.MethodPut, path+"/firewallRules/"+AllowAzureServices, e.version, rule, nil); ferr != nil {
		return "", fmt.Errorf("let Azure's services reach %s: %w", what, ferr)
	}
	if srv.Properties.FQDN == "" {
		return "", fmt.Errorf("azure gave %s no host name", what)
	}
	return srv.Properties.FQDN, nil
}

// GetFlexibleServer reads a flexible server by resource ID; a missing one
// is IsNotFound.
func (s *Session) GetFlexibleServer(ctx context.Context, id string) (*Server, error) {
	e, ok := s.ownsServer(id)
	if !ok {
		return nil, fmt.Errorf("%s isn't a flexible server in subscription %s", id, s.cred.SubscriptionID)
	}
	var srv Server
	if _, err := s.do(ctx, http.MethodGet, id, e.version, nil, &srv); err != nil {
		return nil, err
	}
	return &srv, nil
}

// DeleteFlexibleServer deletes a flexible server, with its data, and
// waits until it's gone. One that's already gone is not an error.
func (s *Session) DeleteFlexibleServer(ctx context.Context, id string) error {
	e, ok := s.ownsServer(id)
	if !ok {
		return fmt.Errorf("%s isn't a flexible server in subscription %s", id, s.cred.SubscriptionID)
	}
	_, err := s.do(ctx, http.MethodDelete, id, e.version, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for {
		_, gerr := s.GetFlexibleServer(ctx, id)
		if IsNotFound(gerr) {
			return nil
		}
		if gerr != nil {
			return gerr
		}
		progress(ctx, "Deleting the "+e.title+" flexible server")
		if werr := s.wait(ctx); werr != nil {
			return werr
		}
	}
}

package digitalocean

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

const infrastructureMaxPages = 100

var _ provider.ServerAdapter = (*Client)(nil)

// infrastructureError excludes remote bodies that may contain credentials or
// bootstrap inputs. Permission failures retain the operation's required scope.
func infrastructureError(err error, permission string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var api *APIError
	if errors.As(err, &api) {
		switch api.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return &provider.InfrastructureScopeError{Provider: "digitalocean", Permission: permission}
		case http.StatusNotFound:
			return provider.ErrInfrastructureNotFound
		default:
			return &provider.InfrastructureAPIError{Provider: "digitalocean", Status: api.Status, RetryAfter: api.RetryAfter}
		}
	}
	return &provider.InfrastructureAPIError{Provider: "digitalocean"}
}

func (c *Client) infrastructureIdentity(ctx context.Context, credential provider.InfrastructureCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if credential.Provider != "digitalocean" || credential.AccountID == "" || CheckToken(credential.Token) != nil {
		return provider.ErrInfrastructureInvalid
	}
	a, err := c.Account(ctx, credential.Token)
	if err != nil {
		return infrastructureError(err, "account:read")
	}
	id := a.UUID
	if a.Team != nil {
		id = a.Team.UUID
	}
	if id == "" || id != credential.AccountID || (credential.TeamID != "" && (a.Team == nil || a.Team.UUID != credential.TeamID)) {
		return provider.ErrInfrastructureIdentity
	}
	return nil
}

func infrastructureID(id string) error {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != id {
		return provider.ErrInfrastructureInvalid
	}
	return nil
}

type droplet struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Size   string `json:"size_slug"`
	Region struct {
		Slug string `json:"slug"`
	} `json:"region"`
	Tags     []string `json:"tags"`
	Networks struct {
		V4 []struct {
			IP   string `json:"ip_address"`
			Type string `json:"type"`
		} `json:"v4"`
		V6 []struct {
			IP   string `json:"ip_address"`
			Type string `json:"type"`
		} `json:"v6"`
	} `json:"networks"`
}

func (d droplet) server(accountID string) provider.InfrastructureServer {
	s := provider.InfrastructureServer{ID: strconv.FormatInt(d.ID, 10), AccountID: accountID, Name: d.Name, Region: d.Region.Slug, Size: d.Size, Status: d.Status, Tags: slices.Clone(d.Tags)}
	for _, n := range d.Networks.V4 {
		if n.Type == "public" {
			s.PublicIPv4 = append(s.PublicIPv4, n.IP)
		}
	}
	for _, n := range d.Networks.V6 {
		if n.Type == "public" {
			s.PublicIPv6 = append(s.PublicIPv6, n.IP)
		}
	}
	return s
}

type infrastructureLinks struct {
	Pages struct {
		Next string `json:"next"`
	} `json:"pages"`
	Actions []struct {
		ID   int64  `json:"id"`
		Href string `json:"href"`
		Rel  string `json:"rel"`
	} `json:"actions"`
}
type dropletAction struct {
	ID         int64  `json:"id"`
	ResourceID int64  `json:"resource_id"`
	Status     string `json:"status"`
	Type       string `json:"type"`
}

func (a dropletAction) action(resourceID string) provider.InfrastructureAction {
	return provider.InfrastructureAction{ID: strconv.FormatInt(a.ID, 10), ResourceID: resourceID, Status: a.Status, Kind: a.Type}
}

// ListServers returns a bounded snapshot in the credential's verified account.
func (c *Client) ListServers(ctx context.Context, credential provider.InfrastructureCredential) ([]provider.InfrastructureServer, error) {
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return nil, err
	}
	return c.listInfrastructureServers(ctx, credential, "")
}
func (c *Client) listInfrastructureServers(ctx context.Context, credential provider.InfrastructureCredential, tag string) ([]provider.InfrastructureServer, error) {
	result := []provider.InfrastructureServer{}
	seen := map[int64]bool{}
	for page := 1; page <= infrastructureMaxPages; page++ {
		var out struct {
			Droplets []droplet           `json:"droplets"`
			Links    infrastructureLinks `json:"links"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if tag != "" {
			q.Set("tag_name", tag)
		}
		if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/droplets", q, nil, &out); err != nil {
			return nil, infrastructureError(err, "droplet:read")
		}
		for _, d := range out.Droplets {
			if d.ID < 1 || seen[d.ID] {
				return nil, provider.ErrInfrastructurePagination
			}
			seen[d.ID] = true
			result = append(result, d.server(credential.AccountID))
		}
		if out.Links.Pages.Next == "" {
			return result, nil
		}
	}
	return nil, provider.ErrInfrastructurePagination
}

// GetServer looks up immutable provider identity, independent of the host name.
func (c *Client) GetServer(ctx context.Context, credential provider.InfrastructureCredential, id string) (provider.InfrastructureServer, error) {
	if err := infrastructureID(id); err != nil {
		return provider.InfrastructureServer{}, err
	}
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return provider.InfrastructureServer{}, err
	}
	return c.getInfrastructureServer(ctx, credential, id)
}
func (c *Client) getInfrastructureServer(ctx context.Context, credential provider.InfrastructureCredential, id string) (provider.InfrastructureServer, error) {
	var out struct {
		Droplet droplet `json:"droplet"`
	}
	if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/droplets/"+id, nil, nil, &out); err != nil {
		return provider.InfrastructureServer{}, infrastructureError(err, "droplet:read")
	}
	if out.Droplet.ID < 1 || strconv.FormatInt(out.Droplet.ID, 10) != id {
		return provider.InfrastructureServer{}, provider.ErrInfrastructureIdentity
	}
	return out.Droplet.server(credential.AccountID), nil
}

func serverSpecTag(spec provider.ServerSpec) (string, error) {
	spec.SSHKeys = slices.Clone(spec.SSHKeys)
	slices.Sort(spec.SSHKeys)
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "loomplane-spec-" + hex.EncodeToString(sum[:]), nil
}

// CreateServer submits a creation intent once, or reconciles it by its ownership
// tags. A lost response is ambiguous even when a follow-up listing is empty.
func (c *Client) CreateServer(ctx context.Context, credential provider.InfrastructureCredential, spec provider.ServerSpec) (provider.ServerResult, error) {
	var result provider.ServerResult
	if strings.TrimSpace(spec.OperationID) == "" || len(spec.OperationID) > 256 || spec.Name == "" || spec.Region == "" || spec.Size == "" || spec.Image == "" {
		return result, provider.ErrInfrastructureInvalid
	}
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return result, err
	}
	tag := provider.ServerOperationTag(spec.OperationID)
	specTag, err := serverSpecTag(spec)
	if err != nil {
		return result, provider.ErrInfrastructureInvalid
	}
	list, err := c.listInfrastructureServers(ctx, credential, tag)
	if err != nil {
		return result, err
	}
	for _, s := range list {
		if !slices.Contains(s.Tags, tag) {
			return result, provider.ErrInfrastructureDrift
		}
		if !slices.Contains(s.Tags, specTag) || s.Name != spec.Name || s.Region != spec.Region || s.Size != spec.Size || result.Server.ID != "" {
			return provider.ServerResult{}, provider.ErrInfrastructureDrift
		}
		result.Server = s
	}
	if result.Server.ID != "" {
		result.Reconciled = true
		result.Actions, err = c.creationActions(ctx, credential, result.Server.ID)
		return result, err
	}
	if spec.ReconcileOnly {
		return result, provider.ErrInfrastructureAmbiguous
	}
	in := struct {
		Name     string   `json:"name"`
		Region   string   `json:"region"`
		Size     string   `json:"size"`
		Image    string   `json:"image"`
		SSHKeys  []string `json:"ssh_keys,omitempty"`
		UserData string   `json:"user_data,omitempty"`
		Tags     []string `json:"tags"`
		Backups  bool     `json:"backups"`
		IPv6     bool     `json:"ipv6"`
	}{Name: spec.Name, Region: spec.Region, Size: spec.Size, Image: spec.Image, SSHKeys: spec.SSHKeys, UserData: spec.UserData, Tags: []string{tag, specTag}, IPv6: true}
	var out struct {
		Droplet droplet             `json:"droplet"`
		Links   infrastructureLinks `json:"links"`
	}
	if err = c.do(ctx, credential.Token, http.MethodPost, "/v2/droplets", nil, in, &out); err != nil {
		safe := infrastructureError(err, "droplet:create (and droplet:read, tag:create)")
		var api *APIError
		if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 && api.Status != http.StatusRequestTimeout {
			return result, safe
		}
		return result, errors.Join(provider.ErrInfrastructureAmbiguous, safe)
	}
	if out.Droplet.ID < 1 || !slices.Contains(out.Droplet.Tags, tag) || !slices.Contains(out.Droplet.Tags, specTag) {
		return result, provider.ErrInfrastructureAmbiguous
	}
	result.Server = out.Droplet.server(credential.AccountID)
	for _, a := range out.Links.Actions {
		if a.ID < 1 {
			return result, provider.ErrInfrastructureAmbiguous
		}
		result.Actions = append(result.Actions, provider.InfrastructureAction{ID: strconv.FormatInt(a.ID, 10), ResourceID: result.Server.ID, Kind: a.Rel, Status: "in-progress"})
	}
	return result, nil
}

// ResizeServer returns the accepted action; callers poll GetServerAction.
func (c *Client) ResizeServer(ctx context.Context, credential provider.InfrastructureCredential, id, size string) (provider.InfrastructureAction, error) {
	var result provider.InfrastructureAction
	if size == "" {
		return result, provider.ErrInfrastructureInvalid
	}
	if _, err := c.GetServer(ctx, credential, id); err != nil {
		return result, err
	}
	var out struct {
		Action dropletAction `json:"action"`
	}
	in := struct {
		Type string `json:"type"`
		Size string `json:"size"`
		Disk bool   `json:"disk"`
	}{Type: "resize", Size: size}
	if err := c.do(ctx, credential.Token, http.MethodPost, "/v2/droplets/"+id+"/actions", nil, in, &out); err != nil {
		return result, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, "droplet:update"))
	}
	if out.Action.ID < 1 || (out.Action.ResourceID != 0 && strconv.FormatInt(out.Action.ResourceID, 10) != id) {
		return result, provider.ErrInfrastructureAmbiguous
	}
	return out.Action.action(id), nil
}

// GetServerAction retrieves one action under its exact Droplet ID.
func (c *Client) GetServerAction(ctx context.Context, credential provider.InfrastructureCredential, id, actionID string) (provider.InfrastructureAction, error) {
	var result provider.InfrastructureAction
	if err := infrastructureID(id); err != nil {
		return result, err
	}
	if err := infrastructureID(actionID); err != nil {
		return result, err
	}
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return result, err
	}
	var out struct {
		Action dropletAction `json:"action"`
	}
	if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/droplets/"+id+"/actions/"+actionID, nil, nil, &out); err != nil {
		return result, infrastructureError(err, "droplet:read")
	}
	if strconv.FormatInt(out.Action.ID, 10) != actionID || strconv.FormatInt(out.Action.ResourceID, 10) != id {
		return result, provider.ErrInfrastructureIdentity
	}
	return out.Action.action(id), nil
}

// DeleteServer requests deletion by immutable ID. Poll GetServer until not found
// before treating cleanup as complete; HTTP 204 only confirms acceptance.
func (c *Client) DeleteServer(ctx context.Context, credential provider.InfrastructureCredential, id string) error {
	if err := infrastructureID(id); err != nil {
		return err
	}
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return err
	}
	if _, err := c.getInfrastructureServer(ctx, credential, id); err != nil {
		if errors.Is(err, provider.ErrInfrastructureNotFound) {
			return nil
		}
		return err
	}
	err := infrastructureError(c.do(ctx, credential.Token, http.MethodDelete, "/v2/droplets/"+id, nil, nil, nil), "droplet:delete")
	if errors.Is(err, provider.ErrInfrastructureNotFound) {
		return nil
	}
	if err != nil {
		return errors.Join(provider.ErrInfrastructureAmbiguous, err)
	}
	return nil
}

// InfrastructureCapabilities lists the implemented account APIs.
func InfrastructureCapabilities() []provider.InfrastructureCapability {
	return []provider.InfrastructureCapability{
		provider.ClusterBootstrapAccess, provider.ClusterCapacityObservation, provider.ClusterCreation, provider.ClusterDiscovery, provider.ClusterMutationObservation, provider.ClusterOptionsRead, provider.ClusterConfigurationUpdate,
		provider.DNSRecordDeletion, provider.DNSRecordDiscovery, provider.DNSRecordUpsert, provider.DNSZoneCreation, provider.DNSZoneDiscovery,
		provider.NodePoolAutoscaling, provider.NodePoolCreation, provider.NodePoolUpdate, provider.ServerDeletion, provider.ServerDiscovery, provider.ServerProvisioning, provider.ServerResize,
	}
}

// creationActions recovers exact action identities after a lost create receipt.
func (c *Client) creationActions(ctx context.Context, credential provider.InfrastructureCredential, id string) ([]provider.InfrastructureAction, error) {
	actions := []provider.InfrastructureAction{}
	seen := map[int64]bool{}
	for page := 1; page <= infrastructureMaxPages; page++ {
		var out struct {
			Actions []dropletAction     `json:"actions"`
			Links   infrastructureLinks `json:"links"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/droplets/"+id+"/actions", q, nil, &out); err != nil {
			return nil, infrastructureError(err, "droplet:read")
		}
		for _, action := range out.Actions {
			if action.ID < 1 || seen[action.ID] {
				return nil, provider.ErrInfrastructurePagination
			}
			seen[action.ID] = true
			if action.ResourceID != 0 && strconv.FormatInt(action.ResourceID, 10) != id {
				return nil, provider.ErrInfrastructureIdentity
			}
			if action.Type == "create" {
				actions = append(actions, action.action(id))
			}
		}
		if out.Links.Pages.Next == "" {
			if len(actions) != 1 {
				return nil, provider.ErrInfrastructureAmbiguous
			}
			return actions, nil
		}
	}
	return nil, provider.ErrInfrastructurePagination
}

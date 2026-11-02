package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ResourceKind identifies a Cloudflare account resource and its native API.
type ResourceKind string

const (
	ResourceD1            ResourceKind = "d1"
	ResourceKV            ResourceKind = "kv"
	ResourceR2            ResourceKind = "r2"
	ResourceQueue         ResourceKind = "queue"
	ResourceVectorize     ResourceKind = "vectorize"
	ResourceHyperdrive    ResourceKind = "hyperdrive"
	ResourceDurableObject ResourceKind = "durable_object"
)

var (
	// ErrInvalidResource means a resource request has an invalid identity or configuration.
	ErrInvalidResource = errors.New("invalid cloudflare resource request")
	// ErrResourceOperationUnsupported marks lifecycle operations that need a Worker class deployment.
	ErrResourceOperationUnsupported = errors.New("cloudflare resource operation requires a Worker class lifecycle deployment")
	resourceHex                     = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
	resourceUUID                    = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
	resourceSegment                 = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	resourceBucket                  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

// Resource contains identity and allowlisted metadata. It never contains data or credentials.
type Resource struct {
	ID        string
	Name      string
	Kind      ResourceKind
	CreatedAt string
	Metadata  map[string]string
}

// HyperdriveOrigin supplies the origin only when creating a configuration.
// Keep this value out of inventory records, audit payloads and logs.
type HyperdriveOrigin struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// CreateResourceOptions describes a native resource. Origin is sent only to Hyperdrive.
type CreateResourceOptions struct {
	Kind            ResourceKind
	Name            string
	Location        string
	Jurisdiction    string
	Dimensions      int
	Metric          string
	Origin          *HyperdriveOrigin `json:"-"`
	CACertificateID string
	SSLMode         string
}

// ListResources returns complete metadata for one resource kind. R2 uses the default jurisdiction.
// Call each kind independently so a denied permission does not hide another kind's inventory.
func (c *Client) ListResources(ctx context.Context, token, accountID string, kind ResourceKind) ([]Resource, error) {
	return c.ListResourcesInJurisdiction(ctx, token, accountID, kind, "")
}

// ListResourcesInJurisdiction lists one kind and scope. R2 jurisdictions are independent inventories.
func (c *Client) ListResourcesInJurisdiction(ctx context.Context, token, accountID string, kind ResourceKind, jurisdiction string) ([]Resource, error) {
	path, err := resourcePath(accountID, kind, "")
	if err != nil {
		return nil, err
	}
	jurisdiction, err = resourceJurisdiction(kind, jurisdiction)
	if err != nil {
		return nil, err
	}
	resources := []Resource{}
	seenIDs := map[string]bool{}
	seenCursors := map[string]bool{}
	cursor := ""
	const perPage = 20
	for page := 1; page <= 10000; page++ {
		query := url.Values{}
		if kind == ResourceR2 {
			query.Set("per_page", strconv.Itoa(perPage))
			if cursor != "" {
				query.Set("cursor", cursor)
			}
		} else if kind != ResourceVectorize {
			query.Set("page", strconv.Itoa(page))
			query.Set("per_page", strconv.Itoa(perPage))
		}
		env, err := c.resourceRequest(ctx, token, http.MethodGet, path, query, jurisdiction, nil)
		if err != nil {
			return nil, err
		}
		var rows []resourceWire
		if kind == ResourceR2 {
			var result struct {
				Buckets []resourceWire `json:"buckets"`
			}
			if err = json.Unmarshal(env.Result, &result); err != nil || result.Buckets == nil {
				return nil, errors.New("cloudflare: invalid bucket inventory response")
			}
			rows = result.Buckets
		} else {
			if err = json.Unmarshal(env.Result, &rows); err != nil || rows == nil {
				return nil, errors.New("cloudflare: invalid resource inventory response")
			}
		}
		for _, row := range rows {
			resource, err := row.resource(kind, jurisdiction)
			if err != nil {
				return nil, err
			}
			if seenIDs[resource.ID] {
				return nil, errors.New("cloudflare: resource inventory repeated an identity")
			}
			seenIDs[resource.ID] = true
			resources = append(resources, resource)
		}
		info := env.ResultInfo
		if kind == ResourceR2 {
			if info.Cursor == "" {
				return resources, nil
			}
			if len(rows) == 0 || seenCursors[info.Cursor] {
				return nil, errors.New("cloudflare: resource inventory cursor did not advance")
			}
			seenCursors[info.Cursor] = true
			cursor = info.Cursor
			continue
		}
		if kind == ResourceVectorize {
			return resources, nil
		}
		if info.Page != 0 && info.Page != page {
			return nil, errors.New("cloudflare: resource inventory page did not advance")
		}
		more := false
		switch {
		case info.TotalPages > 0:
			more = page < info.TotalPages
		case info.TotalCount != nil:
			more = len(resources) < *info.TotalCount
		default:
			size := perPage
			if info.PerPage > 0 {
				size = info.PerPage
			}
			more = len(rows) >= size
		}
		if !more {
			return resources, nil
		}
		if len(rows) == 0 {
			return nil, errors.New("cloudflare: resource inventory ended before its final page")
		}
	}
	return nil, errors.New("cloudflare: resource inventory exceeded its page limit")
}

// GetResource returns one resource from its native account scope.
func (c *Client) GetResource(ctx context.Context, token, accountID string, kind ResourceKind, id string) (Resource, error) {
	return c.GetResourceInJurisdiction(ctx, token, accountID, kind, id, "")
}

// GetResourceInJurisdiction reads a resource in the supplied R2 jurisdiction.
func (c *Client) GetResourceInJurisdiction(ctx context.Context, token, accountID string, kind ResourceKind, id, jurisdiction string) (Resource, error) {
	if err := validateResourceID(kind, id); err != nil {
		return Resource{}, err
	}
	path, err := resourcePath(accountID, kind, id)
	if err != nil {
		return Resource{}, err
	}
	jurisdiction, err = resourceJurisdiction(kind, jurisdiction)
	if err != nil {
		return Resource{}, err
	}
	if kind == ResourceDurableObject {
		list, listErr := c.ListResources(ctx, token, accountID, kind)
		if listErr != nil {
			return Resource{}, listErr
		}
		for _, resource := range list {
			if resource.ID == id {
				return resource, nil
			}
		}
		return Resource{}, &APIError{Status: http.StatusNotFound}
	}
	env, err := c.resourceRequest(ctx, token, http.MethodGet, path, nil, jurisdiction, nil)
	if err != nil {
		return Resource{}, err
	}
	var row resourceWire
	if err = json.Unmarshal(env.Result, &row); err != nil {
		return Resource{}, errors.New("cloudflare: invalid resource response")
	}
	resource, err := row.resource(kind, jurisdiction)
	if err != nil {
		return Resource{}, err
	}
	if resource.ID != id {
		return Resource{}, errors.New("cloudflare: resource identity does not match request")
	}
	return resource, nil
}

// CreateResource creates a native resource. Durable Object classes are created by Worker class lifecycle deployments.
func (c *Client) CreateResource(ctx context.Context, token, accountID string, opts CreateResourceOptions) (Resource, error) {
	path, err := resourcePath(accountID, opts.Kind, "")
	if err != nil {
		return Resource{}, err
	}
	if opts.Kind == ResourceDurableObject {
		return Resource{}, ErrResourceOperationUnsupported
	}
	if (opts.Kind != ResourceHyperdrive && (opts.Origin != nil || opts.CACertificateID != "" || opts.SSLMode != "")) ||
		(opts.Kind != ResourceVectorize && (opts.Dimensions != 0 || opts.Metric != "")) ||
		(opts.Kind != ResourceD1 && opts.Kind != ResourceR2 && opts.Location != "") ||
		(!oneOf(string(opts.Kind), string(ResourceD1), string(ResourceKV), string(ResourceR2), string(ResourceQueue)) && opts.Jurisdiction != "") {
		return Resource{}, ErrInvalidResource
	}
	if !resourceText(opts.Name, 2048) {
		return Resource{}, ErrInvalidResource
	}
	r2Jurisdiction := ""
	if opts.Kind == ResourceR2 {
		r2Jurisdiction = opts.Jurisdiction
	}
	jurisdiction, err := resourceJurisdiction(opts.Kind, r2Jurisdiction)
	if err != nil {
		return Resource{}, err
	}
	body := map[string]any{"name": opts.Name}
	switch opts.Kind {
	case ResourceD1:
		if len(opts.Name) > 255 || !oneOf(opts.Location, "", "wnam", "enam", "weur", "eeur", "apac", "oc") || !oneOf(opts.Jurisdiction, "", "eu", "fedramp", "us") {
			return Resource{}, ErrInvalidResource
		}
		if opts.Location != "" {
			body["primary_location_hint"] = opts.Location
		}
		if opts.Jurisdiction != "" {
			body["jurisdiction"] = opts.Jurisdiction
		}
	case ResourceKV:
		if len(opts.Name) > 512 || !oneOf(opts.Jurisdiction, "", "eu", "fedramp", "us") {
			return Resource{}, ErrInvalidResource
		}
		body = map[string]any{"title": opts.Name}
		if opts.Jurisdiction != "" {
			body["jurisdiction"] = opts.Jurisdiction
		}
	case ResourceR2:
		if !resourceBucket.MatchString(opts.Name) || !oneOf(opts.Location, "", "wnam", "enam", "weur", "eeur", "apac", "oc") {
			return Resource{}, ErrInvalidResource
		}
		if opts.Location != "" {
			body["locationHint"] = opts.Location
		}
	case ResourceQueue:
		if !resourceSegment.MatchString(opts.Name) || !oneOf(opts.Jurisdiction, "", "eu", "us", "fedramp") {
			return Resource{}, ErrInvalidResource
		}
		body = map[string]any{"queue_name": opts.Name}
		if opts.Jurisdiction != "" {
			body["jurisdiction"] = opts.Jurisdiction
		}
	case ResourceVectorize:
		if !resourceSegment.MatchString(opts.Name) || opts.Dimensions < 1 || opts.Dimensions > 1536 || !oneOf(opts.Metric, "cosine", "euclidean", "dot-product") {
			return Resource{}, ErrInvalidResource
		}
		body["config"] = map[string]any{"dimensions": opts.Dimensions, "metric": opts.Metric}
	case ResourceHyperdrive:
		if originErr := validateHyperdriveOrigin(opts.Origin); originErr != nil {
			return Resource{}, originErr
		}
		body["origin"] = opts.Origin
		if opts.CACertificateID != "" || opts.SSLMode != "" {
			mode := "verify-full"
			if opts.Origin.Scheme == "mysql" {
				mode = "VERIFY_IDENTITY"
			}
			if !resourceUUID.MatchString(opts.CACertificateID) || opts.SSLMode != mode {
				return Resource{}, ErrInvalidResource
			}
			body["mtls"] = map[string]string{"ca_certificate_id": opts.CACertificateID, "sslmode": opts.SSLMode}
		}
	}
	env, err := c.resourceRequest(ctx, token, http.MethodPost, path, nil, jurisdiction, body)
	if err != nil {
		return Resource{}, err
	}
	var row resourceWire
	if err = json.Unmarshal(env.Result, &row); err != nil {
		return Resource{}, errors.New("cloudflare: invalid resource create response")
	}
	resource, err := row.resource(opts.Kind, jurisdiction)
	if err != nil {
		return Resource{}, err
	}
	if resource.Name != opts.Name {
		return Resource{}, errors.New("cloudflare: created resource name does not match request")
	}
	return resource, nil
}

// DeleteResource removes a resource in the default jurisdiction.
func (c *Client) DeleteResource(ctx context.Context, token, accountID string, kind ResourceKind, id string) error {
	return c.DeleteResourceInJurisdiction(ctx, token, accountID, kind, id, "")
}

// DeleteResourceInJurisdiction removes a native resource in the supplied R2 jurisdiction.
func (c *Client) DeleteResourceInJurisdiction(ctx context.Context, token, accountID string, kind ResourceKind, id, jurisdiction string) error {
	if err := validateResourceID(kind, id); err != nil {
		return err
	}
	path, err := resourcePath(accountID, kind, id)
	if err != nil {
		return err
	}
	if kind == ResourceDurableObject {
		return ErrResourceOperationUnsupported
	}
	jurisdiction, err = resourceJurisdiction(kind, jurisdiction)
	if err != nil {
		return err
	}
	_, err = c.resourceRequest(ctx, token, http.MethodDelete, path, nil, jurisdiction, nil)
	return err
}

func resourcePath(account string, kind ResourceKind, id string) (string, error) {
	if !resourceHex.MatchString(account) {
		return "", ErrInvalidResource
	}
	var path string
	switch kind {
	case ResourceD1:
		path = "/d1/database"
	case ResourceKV:
		path = "/storage/kv/namespaces"
	case ResourceR2:
		path = "/r2/buckets"
	case ResourceQueue:
		path = "/queues"
	case ResourceVectorize:
		path = "/vectorize/v2/indexes"
	case ResourceHyperdrive:
		path = "/hyperdrive/configs"
	case ResourceDurableObject:
		path = "/workers/durable_objects/namespaces"
	default:
		return "", ErrInvalidResource
	}
	if id != "" {
		if err := validateResourceID(kind, id); err != nil {
			return "", err
		}
		path += "/" + url.PathEscape(id)
	}
	return "/accounts/" + url.PathEscape(account) + path, nil
}

func validateResourceID(kind ResourceKind, id string) error {
	valid := false
	switch kind {
	case ResourceD1:
		valid = resourceUUID.MatchString(id)
	case ResourceKV, ResourceQueue, ResourceHyperdrive, ResourceDurableObject:
		valid = resourceHex.MatchString(id)
	case ResourceR2:
		valid = resourceBucket.MatchString(id)
	case ResourceVectorize:
		valid = resourceSegment.MatchString(id)
	}
	if !valid {
		return ErrInvalidResource
	}
	return nil
}

func resourceJurisdiction(kind ResourceKind, jurisdiction string) (string, error) {
	if kind != ResourceR2 {
		if jurisdiction != "" {
			return "", ErrInvalidResource
		}
		return "", nil
	}
	if jurisdiction == "" {
		jurisdiction = "default"
	}
	if !oneOf(jurisdiction, "default", "eu", "us", "fedramp", "fedramp-high") {
		return "", ErrInvalidResource
	}
	return jurisdiction, nil
}

func resourceText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum && !strings.ContainsFunc(value, unicode.IsControl)
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func validateHyperdriveOrigin(origin *HyperdriveOrigin) error {
	if origin == nil || !oneOf(origin.Scheme, "postgres", "postgresql", "mysql") || !resourceText(origin.Host, 253) || strings.ContainsAny(origin.Host, "/@?# \\:") || origin.Port < 0 || origin.Port > 65535 || !resourceText(origin.Database, 2048) || !resourceText(origin.User, 2048) || origin.Password == "" || len(origin.Password) > 2048 {
		return ErrInvalidResource
	}
	return nil
}

// resourceWire decodes only fields that can be kept in metadata. Unknown provider
// fields, including Hyperdrive origin credentials, never reach a Resource.
type resourceWire struct {
	ID           string `json:"id"`
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	Title        string `json:"title"`
	QueueID      string `json:"queue_id"`
	QueueName    string `json:"queue_name"`
	CreatedAt    string `json:"created_at"`
	CreatedOn    string `json:"created_on"`
	CreationDate string `json:"creation_date"`
	Jurisdiction string `json:"jurisdiction"`
	Location     string `json:"location"`
	Class        string `json:"class"`
	Script       string `json:"script"`
	UseSQLite    *bool  `json:"use_sqlite"`
	Config       struct {
		Dimensions int    `json:"dimensions"`
		Metric     string `json:"metric"`
	} `json:"config"`
}

func (row resourceWire) resource(kind ResourceKind, jurisdiction string) (Resource, error) {
	out := Resource{ID: row.ID, Name: row.Name, Kind: kind, CreatedAt: row.CreatedAt, Metadata: map[string]string{}}
	switch kind {
	case ResourceD1:
		out.ID = row.UUID
	case ResourceKV:
		out.Name = row.Title
	case ResourceR2, ResourceVectorize:
		out.ID = row.Name
	case ResourceDurableObject:
		if out.Name == "" {
			out.Name = row.Class
		}
	case ResourceQueue:
		out.ID = row.QueueID
		out.Name = row.QueueName
	}
	if validateResourceID(kind, out.ID) != nil || !resourceText(out.Name, 2048) {
		return Resource{}, errors.New("cloudflare: invalid resource identity in response")
	}
	if out.CreatedAt == "" {
		out.CreatedAt = row.CreatedOn
	}
	if out.CreatedAt == "" {
		out.CreatedAt = row.CreationDate
	}
	if row.Jurisdiction != "" {
		out.Metadata["jurisdiction"] = row.Jurisdiction
	}
	if kind == ResourceR2 {
		if row.Jurisdiction != "" && row.Jurisdiction != jurisdiction {
			return Resource{}, errors.New("cloudflare: resource jurisdiction does not match request")
		}
		out.Metadata["jurisdiction"] = jurisdiction
	}
	if row.Location != "" {
		out.Metadata["location"] = row.Location
	}
	if kind == ResourceVectorize {
		if row.Config.Dimensions > 0 {
			out.Metadata["dimensions"] = strconv.Itoa(row.Config.Dimensions)
		}
		if row.Config.Metric != "" {
			out.Metadata["metric"] = row.Config.Metric
		}
	}
	if kind == ResourceDurableObject {
		if row.Class != "" {
			out.Metadata["class"] = row.Class
		}
		if row.Script != "" {
			out.Metadata["script"] = row.Script
		}
		if row.UseSQLite != nil {
			out.Metadata["use_sqlite"] = strconv.FormatBool(*row.UseSQLite)
		}
	}
	return out, nil
}

type resourceEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code int `json:"code"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		Page       int    `json:"page"`
		PerPage    int    `json:"per_page"`
		TotalPages int    `json:"total_pages"`
		TotalCount *int   `json:"total_count"`
		Cursor     string `json:"cursor"`
	} `json:"result_info"`
}

// resourceRequest adds R2 scope headers and requires a valid response envelope.
// API error messages and response bodies can contain credentials, so only status and codes escape.
func (c *Client) resourceRequest(ctx context.Context, token, method, path string, query url.Values, jurisdiction string, body any) (resourceEnvelope, error) {
	var env resourceEnvelope
	if err := CheckToken(token); err != nil {
		return env, fmt.Errorf("%w: %w", errNotSent, err)
	}
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return env, fmt.Errorf("%w: invalid resource request body", errNotSent)
		}
		payload = bytes.NewReader(data)
	}
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	return c.resourceHTTP(ctx, token, method, path, query, jurisdiction, payload, contentType)
}

func (c *Client) resourceHTTP(ctx context.Context, token, method, path string, query url.Values, jurisdiction string, payload io.Reader, contentType string) (resourceEnvelope, error) {
	var env resourceEnvelope
	if err := CheckToken(token); err != nil {
		return env, fmt.Errorf("%w: %w", errNotSent, err)
	}
	endpoint := c.endpoint + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return env, fmt.Errorf("%w: invalid resource request URL", errNotSent)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if jurisdiction != "" {
		req.Header.Set("cf-r2-jurisdiction", jurisdiction)
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return env, ctx.Err()
		}
		return env, errors.New("cloudflare: resource request failed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return env, errors.New("cloudflare: invalid resource response body")
	}
	decoded := json.Unmarshal(data, &env) == nil
	if res.StatusCode >= 300 || (decoded && !env.Success) {
		ae := &APIError{Status: res.StatusCode}
		for _, entry := range env.Errors {
			ae.Codes = append(ae.Codes, entry.Code)
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return env, fmt.Errorf("%w: %w", ErrRejected, ae)
		}
		return env, ae
	}
	if method == http.MethodDelete && res.StatusCode == http.StatusNoContent && len(data) == 0 {
		return env, nil
	}
	if !decoded || !env.Success {
		return env, errors.New("cloudflare: invalid resource response envelope")
	}
	if method != http.MethodDelete && (len(env.Result) == 0 || string(env.Result) == "null") {
		return env, errors.New("cloudflare: missing resource response result")
	}
	return env, nil
}

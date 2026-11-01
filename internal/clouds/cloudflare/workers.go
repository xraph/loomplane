package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// WorkerModule is a JavaScript ESM part, relative to the Worker's source directory.
type WorkerModule struct {
	Name    string
	Content []byte
}

// WorkerBinding represents a native runtime binding. Text is used only during upload.
type WorkerBinding struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	ID           string `json:"id,omitempty"`
	NamespaceID  string `json:"namespace_id,omitempty"`
	BucketName   string `json:"bucket_name,omitempty"`
	QueueName    string `json:"queue_name,omitempty"`
	IndexName    string `json:"index_name,omitempty"`
	ClassName    string `json:"class_name,omitempty"`
	ScriptName   string `json:"script_name,omitempty"`
	Text         string `json:"text,omitempty"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
}

// WorkerMigrations creates new local Durable Object classes without deleting existing data.
type WorkerMigrations struct {
	OldTag           string   `json:"old_tag,omitempty"`
	NewTag           string   `json:"new_tag"`
	NewSQLiteClasses []string `json:"new_sqlite_classes,omitempty"`
}

// WorkerUpload is the module and runtime configuration deployed in one API operation.
type WorkerUpload struct {
	MainModule         string            `json:"main_module"`
	CompatibilityDate  string            `json:"compatibility_date"`
	CompatibilityFlags []string          `json:"compatibility_flags,omitempty"`
	Bindings           []WorkerBinding   `json:"bindings"`
	Migrations         *WorkerMigrations `json:"migrations,omitempty"`
	Tags               []string          `json:"tags"`
	Modules            []WorkerModule    `json:"-"`
}

// WorkerScript is safe script metadata. Binding values and source code are excluded.
type WorkerScript struct {
	Name         string   `json:"id"`
	CreatedAt    string   `json:"created_on"`
	ETag         string   `json:"etag"`
	Tags         []string `json:"tags"`
	MigrationTag string   `json:"migration_tag"`
}

// WorkerDeployment identifies the versions currently receiving traffic.
type WorkerDeployment struct {
	ID       string `json:"id"`
	Versions []struct {
		VersionID  string  `json:"version_id"`
		Percentage float64 `json:"percentage"`
	} `json:"versions"`
}

// Revision returns a single version when it receives all traffic, or the deployment ID for a split.
func (d WorkerDeployment) Revision() string {
	if len(d.Versions) == 1 && d.Versions[0].Percentage == 100 {
		return d.Versions[0].VersionID
	}
	return d.ID
}

var workerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var workerBindingNamePattern = regexp.MustCompile(`^[a-zA-Z_$][a-zA-Z0-9_$]*$`)

func workerPath(account, script string) (string, error) {
	if !resourceHex.MatchString(account) || !workerNamePattern.MatchString(script) {
		return "", ErrInvalidResource
	}
	return "/accounts/" + url.PathEscape(account) + "/workers/scripts/" + url.PathEscape(script), nil
}

// ValidWorkerModuleName accepts relative module names without traversal or MIME header characters.
func ValidWorkerModuleName(name string) bool {
	return name != "" && len(name) <= 255 && path.Clean(name) == name && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && name != ".." && !strings.ContainsAny(name, "\\\r\n\x00\"") && (strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".mjs"))
}

// ValidateWorkerBinding rejects invalid native identifiers before a script can change.
func ValidateWorkerBinding(b WorkerBinding) error {
	if !workerBindingNamePattern.MatchString(b.Name) {
		return ErrInvalidResource
	}
	switch b.Type {
	case "plain_text", "secret_text":
		return nil
	case "d1":
		return validateResourceID(ResourceD1, b.ID)
	case "kv_namespace":
		return validateResourceID(ResourceKV, b.NamespaceID)
	case "hyperdrive":
		return validateResourceID(ResourceHyperdrive, b.ID)
	case "r2_bucket":
		if _, err := resourceJurisdiction(ResourceR2, b.Jurisdiction); err != nil {
			return err
		}
		return validateResourceID(ResourceR2, b.BucketName)
	case "queue":
		if !resourceSegment.MatchString(b.QueueName) {
			return ErrInvalidResource
		}
	case "vectorize":
		return validateResourceID(ResourceVectorize, b.IndexName)
	case "durable_object_namespace":
		if !workerBindingNamePattern.MatchString(b.ClassName) || (b.ScriptName != "" && !workerNamePattern.MatchString(b.ScriptName)) {
			return ErrInvalidResource
		}
	default:
		return ErrInvalidResource
	}
	return nil
}

// UploadWorker uploads and immediately deploys ESM modules, bindings and optional class migrations.
func (c *Client) UploadWorker(ctx context.Context, token, account, script string, upload WorkerUpload) (WorkerScript, error) {
	p, err := workerPath(account, script)
	if err != nil {
		return WorkerScript{}, err
	}
	if !ValidWorkerModuleName(upload.MainModule) {
		return WorkerScript{}, ErrInvalidResource
	}
	if _, err = time.Parse("2006-01-02", upload.CompatibilityDate); err != nil {
		return WorkerScript{}, ErrInvalidResource
	}
	if len(upload.Modules) == 0 || len(upload.Modules) > 100 {
		return WorkerScript{}, ErrInvalidResource
	}
	seen := map[string]bool{}
	total := 0
	for _, module := range upload.Modules {
		if !ValidWorkerModuleName(module.Name) || seen[module.Name] || len(module.Content) > 2<<20 {
			return WorkerScript{}, ErrInvalidResource
		}
		total += len(module.Content)
		seen[module.Name] = true
	}
	if total > 10<<20 || !seen[upload.MainModule] {
		return WorkerScript{}, ErrInvalidResource
	}
	names := map[string]bool{}
	for _, binding := range upload.Bindings {
		if ValidateWorkerBinding(binding) != nil || names[binding.Name] {
			return WorkerScript{}, ErrInvalidResource
		}
		names[binding.Name] = true
	}
	if upload.Migrations != nil {
		if !resourceText(upload.Migrations.NewTag, 100) || len(upload.Migrations.NewSQLiteClasses) == 0 {
			return WorkerScript{}, ErrInvalidResource
		}
		for _, class := range upload.Migrations.NewSQLiteClasses {
			if !workerBindingNamePattern.MatchString(class) {
				return WorkerScript{}, ErrInvalidResource
			}
		}
	}
	metadata, err := json.Marshal(upload)
	if err != nil {
		return WorkerScript{}, ErrInvalidResource
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="metadata"`}, "Content-Type": {"application/json"}})
	if err != nil {
		return WorkerScript{}, errors.New("cloudflare: prepare Worker metadata")
	}
	if _, err = part.Write(metadata); err != nil {
		return WorkerScript{}, errors.New("cloudflare: prepare Worker metadata")
	}
	for _, module := range upload.Modules {
		part, err = writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name=%q; filename=%q`, module.Name, module.Name)}, "Content-Type": {"application/javascript+module"}})
		if err != nil {
			return WorkerScript{}, errors.New("cloudflare: prepare Worker module")
		}
		if _, err = part.Write(module.Content); err != nil {
			return WorkerScript{}, errors.New("cloudflare: prepare Worker module")
		}
	}
	if err = writer.Close(); err != nil {
		return WorkerScript{}, errors.New("cloudflare: prepare Worker upload")
	}
	env, err := c.resourceHTTP(ctx, token, http.MethodPut, p, nil, "", &body, writer.FormDataContentType())
	if err != nil {
		return WorkerScript{}, err
	}
	var result WorkerScript
	if json.Unmarshal(env.Result, &result) != nil || result.Name != script {
		return WorkerScript{}, errors.New("cloudflare: Worker upload identity does not match request")
	}
	return result, nil
}

// GetWorkerSettings reads safe ownership tags without returning runtime binding values.
func (c *Client) GetWorkerSettings(ctx context.Context, token, account, script string) (WorkerScript, error) {
	p, err := workerPath(account, script)
	if err != nil {
		return WorkerScript{}, err
	}
	env, err := c.resourceRequest(ctx, token, http.MethodGet, p+"/script-settings", nil, "", nil)
	if err != nil {
		return WorkerScript{}, err
	}
	var result WorkerScript
	if json.Unmarshal(env.Result, &result) != nil {
		return WorkerScript{}, errors.New("cloudflare: invalid Worker settings response")
	}
	result.Name = script
	return result, nil
}

// GetWorkerScript reads script metadata, including the current Durable Object migration tag.
func (c *Client) GetWorkerScript(ctx context.Context, token, account, script string) (WorkerScript, error) {
	if _, err := workerPath(account, script); err != nil {
		return WorkerScript{}, err
	}
	env, err := c.resourceRequest(ctx, token, http.MethodGet, "/accounts/"+url.PathEscape(account)+"/workers/scripts", nil, "", nil)
	if err != nil {
		return WorkerScript{}, err
	}
	var scripts []WorkerScript
	if json.Unmarshal(env.Result, &scripts) != nil || scripts == nil {
		return WorkerScript{}, errors.New("cloudflare: invalid Worker script inventory")
	}
	for _, result := range scripts {
		if result.Name == script {
			return result, nil
		}
	}
	return WorkerScript{}, &APIError{Status: http.StatusNotFound}
}

// GetWorkerDeployment reads the latest deployment actively serving traffic.
func (c *Client) GetWorkerDeployment(ctx context.Context, token, account, script string) (WorkerDeployment, error) {
	p, err := workerPath(account, script)
	if err != nil {
		return WorkerDeployment{}, err
	}
	env, err := c.resourceRequest(ctx, token, http.MethodGet, p+"/deployments", url.Values{"page": {"1"}, "per_page": {"1"}}, "", nil)
	if err != nil {
		return WorkerDeployment{}, err
	}
	var result struct {
		Deployments []WorkerDeployment `json:"deployments"`
	}
	if json.Unmarshal(env.Result, &result) != nil || len(result.Deployments) == 0 {
		return WorkerDeployment{}, errors.New("cloudflare: Worker has no active deployment")
	}
	active := result.Deployments[0]
	if !resourceUUID.MatchString(active.ID) || len(active.Versions) == 0 {
		return WorkerDeployment{}, errors.New("cloudflare: invalid Worker deployment identity")
	}
	total := 0.0
	for _, version := range active.Versions {
		if !resourceUUID.MatchString(version.VersionID) || version.Percentage <= 0 || version.Percentage > 100 {
			return WorkerDeployment{}, errors.New("cloudflare: invalid Worker version identity")
		}
		total += version.Percentage
	}
	if total < 99.999 || total > 100.001 {
		return WorkerDeployment{}, errors.New("cloudflare: invalid Worker traffic allocation")
	}
	return active, nil
}

// AccountWorkerSubdomain reads an existing account subdomain. It never creates or renames it.
func (c *Client) AccountWorkerSubdomain(ctx context.Context, token, account string) (string, error) {
	if !resourceHex.MatchString(account) {
		return "", ErrInvalidResource
	}
	env, err := c.resourceRequest(ctx, token, http.MethodGet, "/accounts/"+url.PathEscape(account)+"/workers/subdomain", nil, "", nil)
	if err != nil {
		return "", err
	}
	var result struct {
		Subdomain string `json:"subdomain"`
	}
	if json.Unmarshal(env.Result, &result) != nil || !workerNamePattern.MatchString(result.Subdomain) {
		return "", errors.New("cloudflare: register a workers.dev account subdomain before deploying this Worker")
	}
	return result.Subdomain, nil
}

// EnableWorkerSubdomain sets the script's public workers.dev availability and confirms the result.
func (c *Client) EnableWorkerSubdomain(ctx context.Context, token, account, script string, enabled bool) (string, error) {
	p, err := workerPath(account, script)
	if err != nil {
		return "", err
	}
	subdomain := ""
	if enabled {
		subdomain, err = c.AccountWorkerSubdomain(ctx, token, account)
		if err != nil {
			return "", err
		}
	}
	env, err := c.resourceRequest(ctx, token, http.MethodPost, p+"/subdomain", nil, "", map[string]bool{"enabled": enabled, "previews_enabled": false})
	if err != nil {
		return "", err
	}
	var result struct {
		Enabled bool `json:"enabled"`
	}
	if json.Unmarshal(env.Result, &result) != nil || result.Enabled != enabled {
		return "", errors.New("cloudflare: Worker subdomain availability does not match request")
	}
	if !enabled {
		return "", nil
	}
	return "https://" + script + "." + subdomain + ".workers.dev", nil
}

// DeleteWorker removes a script without force-deleting referenced Durable Object data.
func (c *Client) DeleteWorker(ctx context.Context, token, account, script string) error {
	p, err := workerPath(account, script)
	if err != nil {
		return err
	}
	_, err = c.resourceRequest(ctx, token, http.MethodDelete, p, nil, "", nil)
	if statusOf(err) == http.StatusNotFound {
		return nil
	}
	return err
}

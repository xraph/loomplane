package catalogconnect

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// APIAccess is a write-only resource access contract. Store the encoded value only in Vault.
type APIAccess struct {
	Provider     string            `json:"provider,omitempty"`
	Kind         string            `json:"kind,omitempty"`
	AccountID    string            `json:"account_id,omitempty"`
	ResourceID   string            `json:"resource_id,omitempty"`
	Incarnation  string            `json:"incarnation,omitempty"`
	ProjectID    string            `json:"project_id,omitempty"`
	Environment  string            `json:"environment,omitempty"`
	Token        string            `json:"token,omitempty"`
	Name         string            `json:"name,omitempty"`
	Jurisdiction string            `json:"jurisdiction,omitempty"`
	ClassName    string            `json:"class_name,omitempty"`
	ScriptName   string            `json:"script_name,omitempty"`
	Native       bool              `json:"native,omitempty"`
	Values       map[string]string `json:"values,omitempty"`
}

var apiIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$`)
var cloudflareAccountPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var apiEnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// IsAPIEngine identifies the supported provider resource access contracts.
func IsAPIEngine(engine string) bool {
	switch engine {
	case "cloudflare-d1", "cloudflare-kv", "cloudflare-r2", "cloudflare-queue", "cloudflare-vectorize", "cloudflare-hyperdrive", "cloudflare-durable-object", "vercel-blob", "vercel-global-config", "vercel-integration":
		return true
	}
	return false
}

func invalidAPIAccess() error {
	return fmt.Errorf("%w: supply supported application access for the selected provider resource", ErrInvalid)
}

// EncodeAPIAccess validates the pinned resource identity before encoding credentials.
func EncodeAPIAccess(a APIAccess) (string, error) {
	if err := validateAPIAccess(a); err != nil {
		return "", err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return "", invalidAPIAccess()
	}
	return string(raw), nil
}

// ParseAPIAccess rejects unknown and duplicate fields, including nested values.
func ParseAPIAccess(raw string) (APIAccess, error) {
	var a APIAccess
	if len(raw) > 128*1024 || strictBucketJSON(raw, &a) != nil {
		return APIAccess{}, invalidAPIAccess()
	}
	if err := validateAPIAccess(a); err != nil {
		return APIAccess{}, err
	}
	return a, nil
}

func validateAPIAccess(a APIAccess) error {
	if a.Kind != "vercel-integration" && (a.ProjectID != "" || a.Environment != "") {
		return invalidAPIAccess()
	}
	if !IsAPIEngine(a.Kind) || !apiIdentityPattern.MatchString(a.AccountID) || !validAPIResourceID(a) {
		return invalidAPIAccess()
	}
	for _, v := range []string{a.Token, a.Name, a.ClassName, a.ScriptName, a.Jurisdiction, a.Incarnation} {
		if len(v) > 16384 || strings.ContainsAny(v, "\r\n\x00") {
			return invalidAPIAccess()
		}
	}
	if len(a.Name) > 256 || a.ClassName != "" && !apiIdentityPattern.MatchString(a.ClassName) || a.ScriptName != "" && !apiIdentityPattern.MatchString(a.ScriptName) || !slices.Contains([]string{"", "default", "eu", "us", "fedramp", "fedramp-high"}, a.Jurisdiction) {
		return invalidAPIAccess()
	}
	if strings.HasPrefix(a.Kind, "cloudflare-") {
		if a.Provider != "cloudflare" || !cloudflareAccountPattern.MatchString(a.AccountID) || len(a.Values) > 0 {
			return invalidAPIAccess()
		}
		if a.Native {
			if (a.Kind == "cloudflare-queue" || a.Kind == "cloudflare-vectorize") && !apiIdentityPattern.MatchString(a.Name) {
				return invalidAPIAccess()
			}
			if a.Kind == "cloudflare-r2" && (!ValidBucketName("cloudflare", a.Name) || !strings.HasSuffix(a.ResourceID, "/"+a.Name)) {
				return invalidAPIAccess()
			}
			if a.Token != "" {
				return invalidAPIAccess()
			}
			if a.Kind == "cloudflare-durable-object" && (a.ClassName == "" || a.ScriptName == "") {
				return invalidAPIAccess()
			}
		} else if a.Token == "" || a.Kind == "cloudflare-durable-object" || a.Kind == "cloudflare-hyperdrive" || a.Kind == "cloudflare-r2" {
			return invalidAPIAccess()
		}
		return nil
	}
	if a.Provider != "vercel" || a.Native || a.Token == "" && a.Kind != "vercel-integration" {
		return invalidAPIAccess()
	}
	if a.Kind == "vercel-integration" {
		if !apiIdentityPattern.MatchString(a.ProjectID) || !apiIdentityPattern.MatchString(a.Environment) || a.Token != "" || len(a.Values) == 0 || len(a.Values) > 64 {
			return invalidAPIAccess()
		}
		for key, v := range a.Values {
			if !apiEnvPattern.MatchString(key) || len(v) > 16384 || v == "" || strings.ContainsRune(v, '\x00') {
				return invalidAPIAccess()
			}
		}
	} else if len(a.Values) > 0 {
		return invalidAPIAccess()
	}
	if a.Kind == "vercel-blob" && blobTokenStore(a.Token) != strings.TrimPrefix(a.ResourceID, "store_") {
		return invalidAPIAccess()
	}
	return nil
}

func blobTokenStore(token string) string {
	parts := strings.SplitN(token, "_", 5)
	if len(parts) != 5 || parts[0] != "vercel" || parts[1] != "blob" || parts[2] != "rw" || parts[4] == "" {
		return ""
	}
	return parts[3]
}

// APIEnvironment lists possible consumer outputs, never credential values.
func APIEnvironment(engine string) []string {
	if strings.HasPrefix(engine, "cloudflare-") && IsAPIEngine(engine) {
		out := []string{"CLOUDFLARE_BINDING"}
		if engine == "cloudflare-hyperdrive" || engine == "cloudflare-durable-object" || engine == "cloudflare-r2" {
			return out
		}
		out = append(out, "CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_API_TOKEN")
		switch engine {
		case "cloudflare-d1":
			out = append(out, "CLOUDFLARE_D1_DATABASE_ID")
		case "cloudflare-kv":
			out = append(out, "CLOUDFLARE_KV_NAMESPACE_ID")
		case "cloudflare-queue":
			out = append(out, "CLOUDFLARE_QUEUE_ID")
		case "cloudflare-vectorize":
			out = append(out, "CLOUDFLARE_VECTORIZE_INDEX")
		}
		return out
	}
	switch engine {
	case "vercel-blob":
		return []string{"BLOB_READ_WRITE_TOKEN"}
	case "vercel-global-config":
		return []string{"GLOBAL_CONFIG"}
	}
	return nil
}

// APIOutputs resolves application access or a credential-free native binding.
func APIOutputs(raw string) (map[string]string, error) {
	a, err := ParseAPIAccess(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if a.Provider == "cloudflare" {
		types := map[string]string{"cloudflare-d1": "d1", "cloudflare-kv": "kv_namespace", "cloudflare-r2": "r2_bucket", "cloudflare-queue": "queue", "cloudflare-vectorize": "vectorize", "cloudflare-hyperdrive": "hyperdrive", "cloudflare-durable-object": "durable_object_namespace"}
		binding := map[string]string{"type": types[a.Kind], "resource_id": a.ResourceID, "account_id": a.AccountID, "kind": a.Kind, "incarnation": a.Incarnation}
		switch a.Kind {
		case "cloudflare-r2":
			binding["bucket_name"] = a.Name
			binding["jurisdiction"] = a.Jurisdiction
		case "cloudflare-queue":
			binding["queue_name"] = a.Name
		case "cloudflare-vectorize":
			binding["index_name"] = a.Name
		case "cloudflare-durable-object":
			binding["class_name"] = a.ClassName
			binding["script_name"] = a.ScriptName
		}
		rawBinding, marshalErr := json.Marshal(binding)
		if marshalErr != nil {
			return nil, invalidAPIAccess()
		}
		out["CLOUDFLARE_BINDING"] = string(rawBinding)
		if a.Native {
			return out, nil
		}
	}
	if a.Provider == "cloudflare" {
		out["CLOUDFLARE_ACCOUNT_ID"], out["CLOUDFLARE_API_TOKEN"] = a.AccountID, a.Token
		key := map[string]string{"cloudflare-d1": "CLOUDFLARE_D1_DATABASE_ID", "cloudflare-kv": "CLOUDFLARE_KV_NAMESPACE_ID", "cloudflare-queue": "CLOUDFLARE_QUEUE_ID", "cloudflare-vectorize": "CLOUDFLARE_VECTORIZE_INDEX"}[a.Kind]
		value := a.ResourceID
		if a.Kind == "cloudflare-vectorize" && a.Name != "" {
			value = a.Name
		}
		out[key] = value
	} else {
		switch a.Kind {
		case "vercel-blob":
			out["BLOB_READ_WRITE_TOKEN"] = a.Token
		case "vercel-global-config":
			out["GLOBAL_CONFIG"] = "https://global-config.vercel.com/" + a.ResourceID + "?token=" + url.QueryEscape(a.Token)
		case "vercel-integration":
			for k, v := range a.Values {
				out[k] = v
			}
		}
	}
	return out, nil
}

func apiHost(a APIAccess) string {
	if a.Provider == "cloudflare" {
		return "api.cloudflare.com"
	}
	switch a.Kind {
	case "vercel-blob":
		return "vercel.com"
	case "vercel-global-config":
		return "global-config.vercel.com"
	}
	return "api.vercel.com"
}
func parseAPIEndpoint(e *Endpoint, raw string) error {
	a, err := ParseAPIAccess(raw)
	if err != nil || a.Kind != e.Engine {
		return invalidAPIAccess()
	}
	e.Host, e.Port, e.Database, e.TLS = apiHost(a), 443, a.ResourceID, TLSVerifyFull
	e.Username = ""
	if a.Kind == "vercel-integration" {
		keys := make([]string, 0, len(a.Values))
		for key := range a.Values {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		e.Username = "bridge:" + a.ProjectID + ";environment:" + a.Environment + ";outputs:" + strings.Join(keys, ",")
	}
	if a.Native {
		e.Username = "native-binding"
	}
	return nil
}
func redactAPIAccess(msg, raw string) string {
	var a APIAccess
	if json.Unmarshal([]byte(raw), &a) != nil {
		return msg
	}
	secrets := []string{a.Token}
	for _, v := range a.Values {
		secrets = append(secrets, v)
	}
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, secret := range secrets {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
			msg = strings.ReplaceAll(msg, url.QueryEscape(secret), "[redacted]")
			escaped, err := json.Marshal(secret)
			if err == nil && len(escaped) > 2 {
				msg = strings.ReplaceAll(msg, string(escaped[1:len(escaped)-1]), "[redacted]")
			}
		}
	}
	return msg
}

func validAPIResourceID(a APIAccess) bool {
	if a.Kind == "cloudflare-r2" {
		parts := strings.Split(a.ResourceID, "/")
		return len(parts) == 3 && parts[0] == "r2" && slices.Contains([]string{"default", "eu", "us", "fedramp", "fedramp-high"}, parts[1]) && ValidBucketName("cloudflare", parts[2]) && (a.Jurisdiction == "" || a.Jurisdiction == parts[1])
	}
	return apiIdentityPattern.MatchString(a.ResourceID)
}

func (APIAccess) String() string   { return "[private provider application access]" }
func (APIAccess) GoString() string { return "[private provider application access]" }

// APIEndpointBridge returns the provider project used to obtain integration credentials.
func APIEndpointBridge(e *Endpoint) string {
	if e == nil || e.Engine != "vercel-integration" {
		return ""
	}
	for _, part := range strings.Split(e.Username, ";") {
		if bridge, ok := strings.CutPrefix(part, "bridge:"); ok && apiIdentityPattern.MatchString(bridge) {
			return bridge
		}
	}
	return ""
}

// APIEndpointOutputs returns validated application output names without access values.
func APIEndpointOutputs(e *Endpoint) []string {
	if e == nil || e.Engine != "vercel-integration" {
		return nil
	}
	for _, part := range strings.Split(e.Username, ";") {
		raw, ok := strings.CutPrefix(part, "outputs:")
		if !ok {
			continue
		}
		if raw == "" {
			return nil
		}
		names := strings.Split(raw, ",")
		if len(names) > 64 {
			return nil
		}
		for _, name := range names {
			if !apiEnvPattern.MatchString(name) {
				return nil
			}
		}
		return names
	}
	return nil
}

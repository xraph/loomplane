package fleet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/pkg/manifest"
)

const MaxTemplateSize = 512 << 10

// ErrTemplate identifies an invalid or unsupported release declaration.
var ErrTemplate = errors.New("invalid fleet template")
var templateName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
var pinnedImage = regexp.MustCompile(`^[a-z0-9][a-z0-9.:/_-]*@sha256:[a-f0-9]{64}$`)

// Template is an operator-authored, artifact-only deployment specification.
// Parameters are data; they never expand into executable text or manifest paths.
type Template struct {
	Config               map[string]map[string]ConfigValue `json:"config,omitempty" yaml:"config,omitempty"`
	SchemaVersion        int                               `json:"schema_version" yaml:"schema_version"`
	Manifest             manifest.Manifest                 `json:"manifest" yaml:"manifest"`
	Parameters           map[string]ParameterSchema        `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Secrets              map[string]SecretSchema           `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	Profiles             map[string]Profile                `json:"profiles" yaml:"profiles"`
	Jobs                 map[string]LifecycleJob           `json:"jobs,omitempty" yaml:"jobs,omitempty"`
	Resources            map[string]ResourcePolicy         `json:"resources,omitempty" yaml:"resources,omitempty"`
	RequiredCapabilities []string                          `json:"required_capabilities,omitempty" yaml:"required_capabilities,omitempty"`
	Compatibility        Compatibility                     `json:"compatibility,omitzero" yaml:"compatibility,omitempty"`
}

type ParameterSchema struct {
	Kind      string   `json:"kind" yaml:"kind"`
	Required  bool     `json:"required,omitempty" yaml:"required,omitempty"`
	MaxLength int      `json:"max_length,omitempty" yaml:"max_length,omitempty"`
	Enum      []string `json:"enum,omitempty" yaml:"enum,omitempty"`
	Minimum   *int64   `json:"minimum,omitempty" yaml:"minimum,omitempty"`
	Maximum   *int64   `json:"maximum,omitempty" yaml:"maximum,omitempty"`
}
type SecretSchema struct {
	Required bool     `json:"required,omitempty" yaml:"required,omitempty"`
	Services []string `json:"services" yaml:"services"`
}
type Profile struct {
	Isolation string                    `json:"isolation" yaml:"isolation"`
	Services  map[string]ServiceProfile `json:"services" yaml:"services"`
}
type ServiceProfile struct {
	CPUMillis   int64 `json:"cpu_millis" yaml:"cpu_millis"`
	MemoryMiB   int64 `json:"memory_mib" yaml:"memory_mib"`
	MinReplicas int   `json:"min_replicas" yaml:"min_replicas"`
	MaxReplicas int   `json:"max_replicas" yaml:"max_replicas"`
}

// LifecycleJob commands come from the operator's immutable release only.
type LifecycleJob struct {
	Phase          string   `json:"phase" yaml:"phase"`
	Image          string   `json:"image" yaml:"image"`
	Command        []string `json:"command,omitempty" yaml:"command,omitempty"`
	Args           []string `json:"args,omitempty" yaml:"args,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds" yaml:"timeout_seconds"`
	MaxAttempts    int      `json:"max_attempts" yaml:"max_attempts"`
	Required       bool     `json:"required,omitempty" yaml:"required,omitempty"`
	SchemaLock     string   `json:"schema_lock,omitempty" yaml:"schema_lock,omitempty"`
}
type ResourcePolicy struct {
	Ownership    string `json:"ownership" yaml:"ownership"`
	AttachmentID string `json:"attachment_id,omitempty" yaml:"attachment_id,omitempty"`
	Version      string `json:"version,omitempty" yaml:"version,omitempty"`
}

// Compatibility uses explicit supported versions, never inferred version ranges.
type Compatibility struct {
	ControlVersions  []string `json:"control_versions,omitempty" yaml:"control_versions,omitempty"`
	ReadDataVersions []string `json:"read_data_versions,omitempty" yaml:"read_data_versions,omitempty"`
	WriteDataVersion string   `json:"write_data_version,omitempty" yaml:"write_data_version,omitempty"`
}

// TemplateRelease is a copy of the immutable published snapshot.
type TemplateRelease struct {
	ID        string    `json:"id" forge:"format=uuid"`
	FleetID   string    `json:"fleet_id" forge:"format=uuid"`
	Digest    string    `json:"digest"`
	Template  Template  `json:"template"`
	CreatedAt time.Time `json:"created_at"`
}

// ParseTemplate rejects unknown fields, duplicate keys and multiple documents.
func ParseTemplate(raw []byte) (*Template, error) {
	if len(raw) == 0 || len(raw) > MaxTemplateSize {
		return nil, ErrTemplate
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var t Template
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTemplate, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: expected one document", ErrTemplate)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// CanonicalDigest includes all fields in the validated template, including images.
func CanonicalDigest(t *Template) (string, error) {
	raw, err := CanonicalTemplate(t)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CanonicalTemplate returns detached bytes suitable for immutable storage.
func CanonicalTemplate(t *Template) ([]byte, error) {
	if t == nil {
		return nil, ErrTemplate
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxTemplateSize {
		return nil, ErrTemplate
	}
	return raw, nil
}

// ArtifactManifest returns a detached manifest with deterministic planner identities.
// These identities are not repository paths and are never sent to a source builder.
func (t *Template) ArtifactManifest() (*manifest.Manifest, error) {
	raw, err := json.Marshal(t.Manifest)
	if err != nil {
		return nil, err
	}
	var m manifest.Manifest
	if decodeErr := json.Unmarshal(raw, &m); decodeErr != nil {
		return nil, decodeErr
	}
	for name, s := range m.Services {
		if s.Source != "" || s.Repo != "" || s.Build == nil || !validImage(s.Build.Image) || s.Build.Context != "" || s.Build.Builder != "" {
			return nil, fmt.Errorf("%w: service %s requires a pinned artifact without source build settings", ErrTemplate, name)
		}
		if !slices.Contains([]string{"http-service", "web-app", "worker"}, s.Kind) {
			return nil, fmt.Errorf("%w: service %s requires an explicit container kind", ErrTemplate, name)
		}
		if len(s.Environments) > 0 {
			return nil, fmt.Errorf("%w: artifact services use profiles, not environment overrides", ErrTemplate)
		}
		s.Source = "artifact/" + name
		m.Services[name] = s
	}
	if err = m.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTemplate, err)
	}
	return &m, nil
}
func validImage(s string) bool { return len(s) <= 512 && pinnedImage.MatchString(s) }

// Validate checks publication contracts before any Cloud capability resolution.
func (t *Template) Validate() error {
	if t == nil || t.SchemaVersion != 1 || len(t.Manifest.Services) == 0 || len(t.Profiles) == 0 || len(t.Profiles) > 64 || len(t.Parameters) > 64 || len(t.Secrets) > 64 || len(t.Jobs) > 32 || len(t.RequiredCapabilities) > 32 {
		return ErrTemplate
	}
	if _, err := t.ArtifactManifest(); err != nil {
		return err
	}
	if err := t.validateConfig(); err != nil {
		return err
	}
	for name, p := range t.Parameters {
		if !templateName.MatchString(name) {
			return ErrTemplate
		}
		switch p.Kind {
		case "string":
			if p.MaxLength < 1 || p.MaxLength > 4096 || p.Minimum != nil || p.Maximum != nil || len(p.Enum) > 100 {
				return ErrTemplate
			}
			seen := map[string]bool{}
			for _, v := range p.Enum {
				if len(v) > p.MaxLength || seen[v] {
					return ErrTemplate
				}
				seen[v] = true
			}
		case "integer":
			if p.Minimum == nil || p.Maximum == nil || *p.Minimum > *p.Maximum || p.MaxLength != 0 || len(p.Enum) > 0 {
				return ErrTemplate
			}
		case "boolean":
			if p.Minimum != nil || p.Maximum != nil || p.MaxLength != 0 || len(p.Enum) > 0 {
				return ErrTemplate
			}
		default:
			return ErrTemplate
		}
	}
	for name, s := range t.Secrets {
		if !templateName.MatchString(name) || len(s.Services) == 0 || len(s.Services) > len(t.Manifest.Services) {
			return ErrTemplate
		}
		seen := map[string]bool{}
		for _, service := range s.Services {
			if _, ok := t.Manifest.Services[service]; !ok || seen[service] {
				return ErrTemplate
			}
			seen[service] = true
		}
	}
	for name, p := range t.Profiles {
		if !templateName.MatchString(name) || !slices.Contains([]string{"namespace", "dedicated-pool", "dedicated-cluster"}, p.Isolation) || len(p.Services) != len(t.Manifest.Services) {
			return ErrTemplate
		}
		for service, r := range p.Services {
			if _, ok := t.Manifest.Services[service]; !ok || r.CPUMillis < 1 || r.CPUMillis > manifest.MaxCPU*1000 || r.MemoryMiB < manifest.MinMemory || r.MemoryMiB > manifest.MaxMemory || r.MinReplicas < 1 || r.MaxReplicas < r.MinReplicas || r.MaxReplicas > manifest.MaxInstances {
				return ErrTemplate
			}
		}
	}
	for name, j := range t.Jobs {
		if !templateName.MatchString(name) || !validImage(j.Image) || !slices.Contains([]string{"pre-deploy", "bootstrap", "verify"}, j.Phase) || j.TimeoutSeconds < 1 || j.TimeoutSeconds > 3600 || j.MaxAttempts < 1 || j.MaxAttempts > 5 || len(j.Command) > 32 || len(j.Args) > 64 || len(j.SchemaLock) > 128 {
			return ErrTemplate
		}
		for _, v := range append(slices.Clone(j.Command), j.Args...) {
			if len(v) > 4096 || strings.ContainsRune(v, 0) {
				return ErrTemplate
			}
		}
	}
	if len(t.Resources) != len(t.Manifest.Resources) {
		return fmt.Errorf("%w: every data resource needs an ownership policy", ErrTemplate)
	}
	for name, r := range t.Resources {
		if _, ok := t.Manifest.Resources[name]; !ok || !slices.Contains([]string{"owned", "attached", "shared"}, r.Ownership) {
			return ErrTemplate
		}
		if r.Ownership == "owned" {
			if r.AttachmentID != "" || t.Manifest.Resources[name].ExistingResource != "" {
				return ErrTemplate
			}
		} else if r.AttachmentID == "" || r.Version == "" {
			return ErrTemplate
		}
	}
	seen := map[string]bool{}
	for _, c := range t.RequiredCapabilities {
		if !templateName.MatchString(c) || seen[c] {
			return ErrTemplate
		}
		seen[c] = true
	}
	for _, versions := range [][]string{t.Compatibility.ControlVersions, t.Compatibility.ReadDataVersions} {
		if len(versions) > 100 {
			return ErrTemplate
		}
		seen = map[string]bool{}
		for _, v := range versions {
			if strings.TrimSpace(v) == "" || len(v) > 128 || seen[v] {
				return ErrTemplate
			}
			seen[v] = true
		}
	}
	if len(t.Compatibility.WriteDataVersion) > 128 {
		return ErrTemplate
	}
	return nil
}

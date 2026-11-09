package fleets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/fleet"
	"github.com/xraph/loomplane/pkg/manifest"
)

// InstanceSnapshot is detached from both the published release and request maps.
// Secret values are resolved only by the authorized destination workflow.
type InstanceSnapshot struct {
	InstanceID     string                                `json:"instance_id"`
	FleetID        string                                `json:"fleet_id"`
	StageID        string                                `json:"stage_id"`
	EnvironmentID  string                                `json:"environment_id"`
	ProfileID      string                                `json:"profile_id"`
	ReleaseID      string                                `json:"release_id"`
	ReleaseDigest  string                                `json:"release_digest"`
	Generation     int64                                 `json:"generation"`
	OwnershipEpoch int64                                 `json:"ownership_epoch"`
	Template       fleet.Template                        `json:"template"`
	Manifest       manifest.Manifest                     `json:"manifest"`
	Parameters     map[string]fleet.ParameterValue       `json:"parameters"`
	SecretRefs     map[string]fleet.SecretRef            `json:"secret_refs"`
	Values         map[string]map[string]string          `json:"values"`
	Secrets        map[string]map[string]fleet.SecretRef `json:"secrets"`
	Digest         string                                `json:"digest"`
}

var secretIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// RenderInstance applies profile sizes and bounded data inputs, never executables.
func RenderInstance(release fleet.TemplateRelease, instance fleet.Instance, input fleet.CreateInstanceInput) (*InstanceSnapshot, error) {
	digest, err := fleet.CanonicalDigest(&release.Template)
	if err != nil {
		return nil, err
	}
	if digest != release.Digest || instance.ID == "" || instance.EnvironmentID == "" || instance.FleetID != release.FleetID || instance.DesiredReleaseID != release.ID || input.ReleaseID != release.ID || instance.StageID != input.StageID || instance.ProfileID != input.ProfileID || instance.DesiredGeneration < 1 || instance.OwnershipEpoch < 1 {
		return nil, ErrInvalid
	}
	raw, err := fleet.CanonicalTemplate(&release.Template)
	if err != nil {
		return nil, err
	}
	template, err := fleet.ParseTemplate(raw)
	if err != nil {
		return nil, err
	}
	profile, ok := template.Profiles[input.ProfileID]
	if !ok {
		return nil, ErrInvalid
	}
	values, err := validateParameters(template, input.Parameters)
	if err != nil {
		return nil, err
	}
	if secretErr := validateSecretRefs(template, input.SecretRefs); secretErr != nil {
		return nil, secretErr
	}
	m, err := template.ArtifactManifest()
	if err != nil {
		return nil, err
	}
	for name, size := range profile.Services {
		service := m.Services[name]
		resolved := &manifest.Size{CPU: float64(size.CPUMillis) / 1000, Memory: int(size.MemoryMiB)}
		if service.Size != nil {
			resolved.GPU, resolved.Shm = service.Size.GPU, service.Size.Shm
		}
		if resolved.Shm > 0 && resolved.Shm >= resolved.Memory {
			return nil, ErrInvalid
		}
		service.Size = resolved
		// A profile's maximum is an admission ceiling, not autoscaling consent.
		service.Scale = &manifest.Scale{Min: size.MinReplicas, Max: size.MinReplicas}
		m.Services[name] = service
	}
	s := &InstanceSnapshot{InstanceID: instance.ID, FleetID: instance.FleetID, StageID: instance.StageID, EnvironmentID: instance.EnvironmentID, ProfileID: instance.ProfileID, ReleaseID: release.ID, ReleaseDigest: release.Digest, Generation: instance.DesiredGeneration, OwnershipEpoch: instance.OwnershipEpoch, Template: *template, Manifest: *m, Parameters: map[string]fleet.ParameterValue{}, SecretRefs: map[string]fleet.SecretRef{}, Values: map[string]map[string]string{}, Secrets: map[string]map[string]fleet.SecretRef{}}
	encoded, err := json.Marshal(input.Parameters)
	if err != nil {
		return nil, err
	}
	if len(input.Parameters) > 0 {
		if decodeErr := json.Unmarshal(encoded, &s.Parameters); decodeErr != nil {
			return nil, decodeErr
		}
	}
	for name, ref := range input.SecretRefs {
		s.SecretRefs[name] = ref
	}
	for _, service := range m.ServiceNames() {
		s.Values[service] = map[string]string{"LOOMPLANE_INSTANCE_ID": instance.ID, "LOOMPLANE_FLEET_ID": instance.FleetID, "LOOMPLANE_ENVIRONMENT_ID": instance.EnvironmentID}
		s.Secrets[service] = map[string]fleet.SecretRef{}
		for key, source := range template.Config[service] {
			switch {
			case source.Parameter != "":
				if value, present := values[source.Parameter]; present {
					s.Values[service][key] = value
				}
			case source.Secret != "":
				if ref, present := input.SecretRefs[source.Secret]; present {
					s.Secrets[service][key] = ref
				}
			case source.Literal != nil:
				s.Values[service][key] = *source.Literal
			}
		}
	}
	s.Digest, err = SnapshotDigest(s)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// SnapshotDigest binds the rendered manifest, input versions and instance identity.
func SnapshotDigest(snapshot *InstanceSnapshot) (string, error) {
	if snapshot == nil {
		return "", ErrInvalid
	}
	copySnapshot := *snapshot
	copySnapshot.Digest = ""
	raw, err := json.Marshal(copySnapshot)
	if err != nil {
		return "", err
	}
	if len(raw) > 2*fleet.MaxTemplateSize {
		return "", ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func validateParameters(t *fleet.Template, inputs map[string]fleet.ParameterValue) (map[string]string, error) {
	out := map[string]string{}
	for name, in := range inputs {
		schema, ok := t.Parameters[name]
		if !ok || in.Kind != schema.Kind {
			return nil, ErrInvalid
		}
		count := 0
		if in.StringValue != nil {
			count++
		}
		if in.IntValue != nil {
			count++
		}
		if in.BoolValue != nil {
			count++
		}
		if count != 1 {
			return nil, ErrInvalid
		}
		switch schema.Kind {
		case "string":
			if in.StringValue == nil || len(*in.StringValue) > schema.MaxLength || strings.ContainsRune(*in.StringValue, 0) || (len(schema.Enum) > 0 && !slices.Contains(schema.Enum, *in.StringValue)) {
				return nil, ErrInvalid
			}
			out[name] = *in.StringValue
		case "integer":
			if in.IntValue == nil || *in.IntValue < *schema.Minimum || *in.IntValue > *schema.Maximum {
				return nil, ErrInvalid
			}
			out[name] = strconv.FormatInt(*in.IntValue, 10)
		case "boolean":
			if in.BoolValue == nil {
				return nil, ErrInvalid
			}
			out[name] = strconv.FormatBool(*in.BoolValue)
		default:
			return nil, ErrInvalid
		}
	}
	for name, schema := range t.Parameters {
		if _, ok := inputs[name]; schema.Required && !ok {
			return nil, fmt.Errorf("%w: required parameter is missing", ErrInvalid)
		}
	}
	return out, nil
}
func validateSecretRefs(t *fleet.Template, refs map[string]fleet.SecretRef) error {
	for name, ref := range refs {
		if _, ok := t.Secrets[name]; !ok || !secretIDPattern.MatchString(ref.ID) || ref.Version < 1 {
			return ErrInvalid
		}
	}
	for name, schema := range t.Secrets {
		if _, ok := refs[name]; schema.Required && !ok {
			return fmt.Errorf("%w: required secret reference is missing", ErrInvalid)
		}
	}
	return nil
}

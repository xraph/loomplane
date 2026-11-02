package deployments

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/planning"
)

// CloudflareWorkerOwnedReceipt records the script and local namespaces created by Loomplane.
type CloudflareWorkerOwnedReceipt struct {
	AccountID       string                           `json:"account_id"`
	ScriptName      string                           `json:"script_name"`
	ScriptCreatedAt string                           `json:"script_created_at"`
	OwnerTag        string                           `json:"owner_tag"`
	ConfigDigest    string                           `json:"config_digest"`
	Namespaces      []CloudflareWorkerOwnedNamespace `json:"namespaces,omitempty"`
}

// CloudflareWorkerOwnedNamespace is an immutable namespace created with the owned Worker's code.
type CloudflareWorkerOwnedNamespace struct {
	ID               string `json:"id"`
	ClassName        string `json:"class_name"`
	CreatedInRelease string `json:"created_in_release"`
}

func cloudflarePriorNamespaces(b *planning.Binding, state cloudflareWorkerState) ([]CloudflareWorkerOwnedNamespace, error) {
	if b == nil || len(b.OwnedResources["cloudflare-worker"]) == 0 {
		return nil, nil
	}
	var receipt CloudflareWorkerOwnedReceipt
	var previous planning.ServiceConfig
	if json.Unmarshal(b.Config, &previous) != nil {
		return nil, errors.New("Cloudflare Worker ownership config is invalid")
	}
	if json.Unmarshal(b.OwnedResources["cloudflare-worker"], &receipt) != nil || receipt.AccountID != state.Account || receipt.ScriptName != state.Script || receipt.ScriptCreatedAt != state.CreatedAt || receipt.OwnerTag != state.Owner || receipt.ConfigDigest != cloudflareConfigurationDigest(b.Name, &previous) {
		return nil, errors.New("Cloudflare Worker ownership receipt no longer matches its original script")
	}
	return receipt.Namespaces, nil
}

func cloudflareConfirmNamespaces(state *cloudflareWorkerState, namespaces []cloudflare.Resource) error {
	for _, owned := range state.Namespaces {
		found := false
		for _, native := range namespaces {
			if native.ID == owned.ID && native.Metadata["class"] == owned.ClassName && native.Metadata["script"] == state.Script {
				found = true
				break
			}
		}
		if !found {
			return errors.New("an owned Cloudflare Durable Object namespace changed or disappeared")
		}
	}
	for _, native := range namespaces {
		class := native.Metadata["class"]
		if native.Metadata["script"] != state.Script || !slices.Contains(state.CreatedClasses, class) {
			continue
		}
		found := false
		for _, owned := range state.Namespaces {
			if owned.ClassName == class {
				if owned.ID != native.ID {
					return errors.New("a Cloudflare Durable Object class has a different namespace identity")
				}
				found = true
				break
			}
		}
		if !found {
			state.Namespaces = append(state.Namespaces, CloudflareWorkerOwnedNamespace{ID: native.ID, ClassName: class, CreatedInRelease: state.Release})
		}
	}
	return nil
}

func cloudflareWorkerOwnedJSON(state cloudflareWorkerState) (string, error) {
	receipt := CloudflareWorkerOwnedReceipt{AccountID: state.Account, ScriptName: state.Script, ScriptCreatedAt: state.CreatedAt, OwnerTag: state.Owner, ConfigDigest: state.Configuration, Namespaces: state.Namespaces}
	encoded, err := json.Marshal(map[string]any{"cloudflare-worker": receipt})
	return string(encoded), err
}

// CloudflareResourceOwnedReceipt pins a managed resource to its reviewed deployment configuration.
type CloudflareResourceOwnedReceipt struct {
	AccountID       string `json:"account_id"`
	AccountIdentity string `json:"account_identity"`
	ConnectionID    string `json:"connection_id"`
	OrgID           string `json:"org_id"`
	ProjectID       string `json:"project_id"`
	EnvironmentID   string `json:"environment_id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	ResourceID      string `json:"resource_id"`
	Incarnation     string `json:"incarnation"`
	ConfigDigest    string `json:"config_digest"`
}

func cloudflareConfigurationDigest(name string, config any) string {
	data, err := json.Marshal(struct {
		Name   string
		Config any
	}{name, config})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func cloudflareResourceOwnedJSON(job *Job, resource cloudflare.Resource, account string) (string, error) {
	incarnation := resource.ID
	if resource.Kind == cloudflare.ResourceR2 || resource.Kind == cloudflare.ResourceVectorize {
		incarnation = resource.CreatedAt
	}
	receipt := CloudflareResourceOwnedReceipt{AccountID: account, AccountIdentity: job.Action.Resource.AccountIdentity, ConnectionID: job.Connection.ID, OrgID: job.Deployment.OrgID, ProjectID: job.Deployment.ProjectID, EnvironmentID: job.Deployment.EnvironmentID, Name: job.Action.Name, Kind: job.Action.Resource.Kind, ResourceID: resource.ID, Incarnation: incarnation, ConfigDigest: cloudflareConfigurationDigest(job.Action.Name, job.Action.Resource)}
	data, err := json.Marshal(map[string]any{"cloudflare-resource": receipt})
	return string(data), err
}

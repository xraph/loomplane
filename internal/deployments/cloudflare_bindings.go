package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
)

func (c *Cloudflare) cloudflareWorkerBindings(ctx context.Context, job *Job, account string) ([]cloudflare.WorkerBinding, error) {
	bindings := []cloudflare.WorkerBinding{}
	names := map[string]bool{}
	nativeKeys := map[string]bool{}
	for _, link := range job.Action.Service.Env {
		if link.Field != CloudflareBindingOutput {
			continue
		}
		if nativeKeys[link.Key] {
			return nil, errors.New("a native Worker binding is declared more than once")
		}
		nativeKeys[link.Key] = true
		var descriptor cloudflareNativeBinding
		if json.Unmarshal([]byte(job.Env[link.Key]), &descriptor) != nil || descriptor.AccountID != account {
			return nil, errors.New("the native Worker binding does not belong to this Cloudflare account")
		}
		kind, err := cloudflareResourceKind(descriptor.Kind)
		if err != nil {
			return nil, err
		}
		id := descriptor.ResourceID
		jurisdiction := ""
		if kind == cloudflare.ResourceR2 {
			jurisdiction = descriptor.Jurisdiction
			if jurisdiction == "" && strings.HasPrefix(id, "r2/") {
				parts := strings.Split(id, "/")
				if len(parts) != 3 || parts[2] != descriptor.BucketName {
					return nil, errors.New("the native R2 binding identity is invalid")
				}
				jurisdiction = parts[1]
			}
			if jurisdiction == "" {
				jurisdiction = "default"
			}
			canonical := "r2/" + jurisdiction + "/" + descriptor.BucketName
			if id != canonical && id != descriptor.BucketName {
				return nil, errors.New("the native R2 binding identity and jurisdiction disagree")
			}
			id = descriptor.BucketName
		}
		current, err := c.Client.GetResourceInJurisdiction(ctx, string(job.Credential), account, kind, id, jurisdiction)
		if err != nil {
			return nil, err
		}
		expected := cloudflareResourceDescriptor(account, descriptor.Kind, current)
		if ((kind == cloudflare.ResourceR2 || kind == cloudflare.ResourceVectorize) && descriptor.Incarnation == "") || (descriptor.Incarnation != "" && descriptor.Incarnation != expected.Incarnation) {
			return nil, errors.New("the native Worker resource incarnation changed")
		}
		if descriptor.Type != expected.Type || descriptor.QueueName != "" && descriptor.QueueName != expected.QueueName || descriptor.IndexName != "" && descriptor.IndexName != expected.IndexName || descriptor.ClassName != expected.ClassName || descriptor.ScriptName != expected.ScriptName {
			return nil, errors.New("the native Worker binding no longer matches its reviewed resource")
		}
		binding := cloudflare.WorkerBinding{Type: expected.Type, Name: link.Key}
		switch kind {
		case cloudflare.ResourceD1, cloudflare.ResourceHyperdrive:
			binding.ID = current.ID
		case cloudflare.ResourceKV:
			binding.NamespaceID = current.ID
		case cloudflare.ResourceR2:
			binding.BucketName = current.Name
			binding.Jurisdiction = jurisdiction
		case cloudflare.ResourceQueue:
			binding.QueueName = current.Name
		case cloudflare.ResourceVectorize:
			binding.IndexName = current.Name
		case cloudflare.ResourceDurableObject:
			if expected.ClassName == "" || expected.ScriptName == "" {
				return nil, errors.New("a Durable Object binding needs its registered class and owning Worker script")
			}
			binding.ClassName, binding.ScriptName = expected.ClassName, expected.ScriptName
		}
		if cloudflare.ValidateWorkerBinding(binding) != nil || names[binding.Name] {
			return nil, errors.New("the native Worker binding name or provider identity is invalid")
		}
		bindings = append(bindings, binding)
		names[binding.Name] = true
	}
	for _, class := range job.Action.Service.Worker.DurableObjects {
		binding := cloudflare.WorkerBinding{Type: "durable_object_namespace", Name: class.Name, ClassName: class.ClassName}
		if cloudflare.ValidateWorkerBinding(binding) != nil || names[class.Name] {
			return nil, errors.New("the Durable Object class binding is invalid or duplicated")
		}
		bindings = append(bindings, binding)
		names[binding.Name] = true
	}
	keys := make([]string, 0, len(job.Env))
	for key := range job.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if nativeKeys[key] {
			continue
		}
		if names[key] {
			return nil, errors.New("an environment variable conflicts with a native Worker binding")
		}
		binding := cloudflare.WorkerBinding{Type: "plain_text", Name: key, Text: job.Env[key]}
		if job.Secrets[key] {
			binding.Type = "secret_text"
		}
		if cloudflare.ValidateWorkerBinding(binding) != nil {
			return nil, errors.New("worker environment variable names must be JavaScript identifiers")
		}
		bindings = append(bindings, binding)
		names[key] = true
	}
	return bindings, nil
}

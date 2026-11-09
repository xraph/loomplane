package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/planning"
)

// externalBaseline keeps the previous image and application settings while
// bringing resource access forward to the release's approved inputs. Azure
// revisions are immutable, so changed access needs a new baseline revision.
func (c *azureCanary) externalBaseline(ctx context.Context, job *Job, baseline *azure.Revision) error {
	var previous planning.ServiceConfig
	if job.Binding != nil && len(job.Binding.Config) > 0 {
		if err := json.Unmarshal(job.Binding.Config, &previous); err != nil {
			return fmt.Errorf("read the baseline's resource variables: %w", err)
		}
	}
	external := func(links []planning.EnvLink) bool {
		return slices.ContainsFunc(links, func(link planning.EnvLink) bool {
			action := approvedServiceResource(job.Plan, link.From)
			return link.Subject == planning.ResourceSubject && action != nil && action.Resource != nil && action.Resource.ExistingResource != nil
		})
	}
	if previous.TargetAccountIdentity == "" && !external(previous.Env) && !external(job.Action.Service.Env) {
		return nil
	}
	remove := map[string]bool{}
	for _, links := range [][]planning.EnvLink{previous.Env, job.Action.Service.Env} {
		for _, link := range links {
			if link.Subject == planning.ResourceSubject {
				for _, key := range []string{link.Key, link.Key + GatewaySuffix, link.Key + DirectSuffix} {
					remove[key] = true
				}
			}
		}
	}
	values := map[string]string{}
	for _, link := range job.Action.Service.Env {
		if link.Subject != planning.ResourceSubject {
			continue
		}
		value, present := job.Env[link.Key]
		if !present {
			return fmt.Errorf("the approved resource variable %s is missing", link.Key)
		}
		if !omitEmptyResourceToken(link.Key, job.Env, job.Action.Service.Env) {
			values[link.Key] = value
		}
		for _, key := range []string{link.Key + GatewaySuffix, link.Key + DirectSuffix} {
			if value, present := job.Env[key]; present {
				values[key] = value
			}
		}
	}
	// An explicit release variable that replaced a removed resource link is
	// still the current approved value, rather than the historical secret.
	for key := range remove {
		if value, present := job.Env[key]; present && !omitEmptyResourceToken(key, job.Env, job.Action.Service.Env) {
			values[key] = value
		}
	}
	stored, err := c.s.ListSecrets(ctx, c.app)
	if err != nil {
		return fmt.Errorf("read baseline resource access: %w", err)
	}
	secrets := map[string]string{}
	for _, secret := range stored {
		secrets[secret.Name] = secret.Value
	}
	matches := func(revision *azure.Revision) bool {
		if !revision.Up() {
			return false
		}
		main := slices.IndexFunc(revision.Properties.Template.Containers, func(container azure.Container) bool { return container.Name == azureContainer })
		if main < 0 {
			return false
		}
		current := map[string]azure.EnvVar{}
		for _, variable := range revision.Properties.Template.Containers[main].Env {
			current[variable.Name] = variable
		}
		for key := range remove {
			want, required := values[key]
			have, present := current[key]
			if !required {
				if present {
					return false
				}
				continue
			}
			value, stored := secrets[have.SecretRef]
			if !present || have.SecretRef == "" || !stored || value != want {
				return false
			}
		}
		return true
	}
	if matches(baseline) {
		return nil
	}
	// Candidate preparation may have failed after its recovery baseline took
	// over but before the runner persisted the target. Adopt that exact
	// approved recovery before any traffic write, leaving the old credentials
	// out of every retry's intermediate traffic state.
	app, err := c.s.GetApp(ctx, c.app)
	if err != nil {
		return err
	}
	if serving := azureServing(app); !strings.EqualFold(serving, baseline.Name) {
		if serving == "" {
			return fmt.Errorf("the canary's traffic is unsettled; restoring its historical resource access is unsafe")
		}
		suffix := strings.TrimPrefix(serving, c.name+"--")
		owned := false
		for retry := 0; retry <= job.Deployment.Retries; retry++ {
			base := azureSuffix("b", job.Deployment.ID, retry)
			for n := 1; n <= 20; n++ {
				owned = owned || suffix == numberedRevision(base, n)
			}
		}
		if owned {
			recovery, recoveryErr := c.revision(ctx, serving)
			if recoveryErr != nil {
				return recoveryErr
			}
			if !matches(recovery) || !recovery.SameTemplateExceptEnv(baseline, azureContainer, slices.Sorted(maps.Keys(remove))) {
				return fmt.Errorf("the serving recovery baseline no longer matches the approved resource access or baseline template")
			}
			c.baseline = recovery.Name
			if recorded := job.Deployment.Canary.service(job.Action.ID); recorded != nil && recorded.Baseline == baseline.Name {
				c.retainedSource = baseline.Name
			}
			return nil
		}
		return fmt.Errorf("the serving revision isn't this canary's approved recovery baseline; its traffic was left unchanged")
	}
	suffix, err := c.freeSuffix(ctx, azureSuffix("b", job.Deployment.ID, job.Deployment.Retries))
	if err != nil {
		return err
	}
	var env []azure.EnvVar
	var additions []azure.Secret
	for _, key := range slices.Sorted(maps.Keys(values)) {
		name := azureSecretName(key) + "-" + suffix
		env = append(env, azure.EnvVar{Name: key, SecretRef: name})
		additions = append(additions, azure.Secret{Name: name, Value: values[key]})
	}
	// Pin traffic before copying: a newly ready revision must not inherit a
	// latest-revision traffic rule while its access is being checked.
	if err := c.s.SetTraffic(ctx, c.app, azure.MultipleRevisions, c.alone()); err != nil {
		return err
	}
	if err := c.s.CopyRevisionEnv(ctx, c.app, baseline.Name, suffix, azureContainer, slices.Sorted(maps.Keys(remove)), env, additions); err != nil {
		return fmt.Errorf("prepare the baseline with approved resource access: %w", err)
	}
	recovery := c.name + "--" + suffix
	if err := c.s.SetTraffic(ctx, c.app, azure.MultipleRevisions, []azure.TrafficWeight{{RevisionName: recovery, Weight: 100}}); err != nil {
		return fmt.Errorf("give the approved recovery baseline traffic: %w", err)
	}
	if recorded := job.Deployment.Canary.service(job.Action.ID); recorded != nil && recorded.Baseline == baseline.Name {
		c.retainedSource = baseline.Name
	}
	c.baseline = recovery
	return nil
}

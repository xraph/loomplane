package kubernetes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

var ErrRegistryPending = errors.New("registry operation is waiting")

const registryRotation = "loomplane.registry.rotation"
const registryRotationGrace = "loomplane.registry.rotation-grace"

// RotateManagedRegistry keeps both users valid while workload pull Secrets
// move to the new credential. Its Secret records every restart-safe phase.
func (c *Client) RotateManagedRegistry(ctx context.Context, org, connection, operation string, now time.Time) error {
	secret, err := c.registrySecret(ctx, org, connection, false)
	if err != nil {
		return err
	}
	name, identity, err := registryIdentity(org, connection, c.Config.Details["namespace_uid"])
	if err != nil {
		return err
	}
	api := c.Kube.CoreV1().Secrets(c.Config.Namespace)
	if secret.Annotations[registryRotation+"-complete"] == operation {
		return nil
	}
	if pending := secret.Annotations[registryRotation]; pending != "" && pending != operation {
		return ErrOwned
	}
	if secret.Annotations[registryRotation] != operation {
		password := rand.Text() + rand.Text()
		user := "loomplane-" + rand.Text()[:12]
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if hashErr != nil {
			return hashErr
		}
		config, marshalErr := json.Marshal(map[string]any{"auths": map[string]any{c.Config.Details["registry_hostname"]: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(user + ":" + password))}}})
		if marshalErr != nil {
			return marshalErr
		}
		secret.Data["next-config.json"] = config
		secret.Data["next-htpasswd"] = []byte(user + ":" + string(hash) + "\n")
		secret.Data["htpasswd"] = append(secret.Data["htpasswd"], secret.Data["next-htpasswd"]...)
		secret.Annotations[registryRotation] = operation
		if secret, err = api.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if len(secret.Data["next-config.json"]) == 0 {
		return c.finishRegistryRotation(ctx, name, identity, secret, operation)
	}
	if operationErr := c.restartRegistryAuth(ctx, name, identity, secret); operationErr != nil {
		return operationErr
	}
	secret.Data["config.json"] = secret.Data["next-config.json"]
	if secret, err = api.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return err
	}
	pulls, err := api.List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(map[string]string{owned.LabelOrg: org, owned.LabelConnection: connection}).String()})
	if err != nil {
		return err
	}
	for i := range pulls.Items {
		pull := &pulls.Items[i]
		if pull.Type != corev1.SecretTypeDockerConfigJson || !strings.HasPrefix(pull.Name, "lp-") || !strings.HasSuffix(pull.Name, "-pull") {
			continue
		}
		var cfg struct {
			Auths map[string]json.RawMessage `json:"auths"`
		}
		if json.Unmarshal(pull.Data[corev1.DockerConfigJsonKey], &cfg) != nil || len(cfg.Auths) != 1 || cfg.Auths[c.Config.Details["registry_hostname"]] == nil {
			continue
		}
		if pull.DeletionTimestamp != nil {
			return ErrOwned
		}
		pull.Data[corev1.DockerConfigJsonKey] = secret.Data["config.json"]
		if _, err = api.Update(ctx, pull, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	deadline, parseErr := time.Parse(time.RFC3339, secret.Annotations[registryRotationGrace])
	if parseErr != nil {
		secret.Annotations[registryRotationGrace] = now.Add(10 * time.Minute).UTC().Format(time.RFC3339)
		_, err = api.Update(ctx, secret, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		return ErrRegistryPending
	}
	if now.Before(deadline) {
		return ErrRegistryPending
	}
	secret.Data["htpasswd"] = secret.Data["next-htpasswd"]
	delete(secret.Data, "next-config.json")
	delete(secret.Data, "next-htpasswd")
	if secret, err = api.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return c.finishRegistryRotation(ctx, name, identity, secret, operation)
}

func (c *Client) finishRegistryRotation(ctx context.Context, name string, identity map[string]string, secret *corev1.Secret, operation string) error {
	if operationErr := c.restartRegistryAuth(ctx, name, identity, secret); operationErr != nil {
		return operationErr
	}
	secret.Annotations[registryRotation+"-complete"] = operation
	delete(secret.Annotations, registryRotation)
	delete(secret.Annotations, registryRotationGrace)
	_, err := c.Kube.CoreV1().Secrets(c.Config.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

func (c *Client) restartRegistryAuth(ctx context.Context, name string, identity map[string]string, secret *corev1.Secret) error {
	api := c.Kube.AppsV1().Deployments(c.Config.Namespace)
	d, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if operationErr := managedOwner(d, identity, c.Config.Details["registry_hostname"]); operationErr != nil {
		return operationErr
	}
	if d.Spec.Template.Annotations == nil {
		d.Spec.Template.Annotations = map[string]string{}
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(secret.Data["htpasswd"]))
	if d.Spec.Template.Annotations["loomplane.registry.auth"] != digest {
		d.Spec.Template.Annotations["loomplane.registry.auth"] = digest
		if _, err = api.Update(ctx, d, metav1.UpdateOptions{}); err != nil {
			return err
		}
		return ErrRegistryPending
	}
	if !deploymentReady(d) {
		return ErrRegistryPending
	}
	return nil
}

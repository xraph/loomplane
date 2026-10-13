package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ErrRejected marks authentication refused by the selected cluster.
var ErrRejected = errors.New("the cluster refused the credential")

// Client owns a single explicitly configured cluster/namespace connection.
type Client struct {
	Config *Config
	Kube   kubeclient.Interface
}

// Result contains only safe metadata and missing permissions.
type Result struct {
	Details map[string]string
	Missing []string
}

// New builds clients without consulting kubeconfig files or environment tokens.
func New(cfg *Config) (*Client, error) {
	if cfg == nil || cfg.REST == nil || cfg.Namespace == "" {
		return nil, ErrInvalid
	}
	rc := rest.CopyConfig(cfg.REST)
	rc.Timeout, rc.QPS, rc.Burst = 15*time.Second, 20, 40
	rc.ContentType, rc.AcceptContentTypes = "application/json", "application/json"
	client, err := kubeclient.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	copyConfig := *cfg
	copyConfig.REST, copyConfig.Details = rc, maps.Clone(cfg.Details)
	if copyConfig.Details == nil {
		copyConfig.Details = map[string]string{}
	}
	return &Client{Config: &copyConfig, Kube: client}, nil
}

// Check validates namespace access and each lifecycle permission. Optional
// ingress/HPA checks do not make a fixed private workload incomplete.
func (c *Client) Check(ctx context.Context) (*Result, error) {
	out := &Result{Details: maps.Clone(c.Config.Details), Missing: []string{}}
	info := &version.Info{}
	data, err := c.Kube.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if apierrors.IsUnauthorized(err) {
		return nil, ErrRejected
	}
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes version: %w", err)
	}
	if err = json.Unmarshal(data, info); err != nil {
		return nil, fmt.Errorf("read Kubernetes version: %w", err)
	}
	out.Details["version"] = info.GitVersion
	ns, err := c.Kube.CoreV1().Namespaces().Get(ctx, c.Config.Namespace, metav1.GetOptions{})
	switch {
	case apierrors.IsUnauthorized(err):
		return nil, ErrRejected
	case apierrors.IsForbidden(err):
		out.Missing = append(out.Missing, "namespaces:get")
	case apierrors.IsNotFound(err):
		out.Missing = append(out.Missing, "namespace:exists")
	case err != nil:
		return nil, fmt.Errorf("read the selected namespace: %w", err)
	default:
		out.Details["namespace_uid"] = string(ns.UID)
	}
	for _, resource := range []struct {
		group, name string
		verbs       []string
	}{
		{"apps", "deployments", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"", "services", []string{"get", "create", "update", "patch", "delete"}},
		{"", "secrets", []string{"get", "create", "update", "patch", "delete"}},
		{"", "pods", []string{"get", "list", "watch"}},
	} {
		for _, verb := range resource.verbs {
			allowed, checkErr := c.allowed(ctx, resource.group, resource.name, verb)
			if checkErr != nil {
				return nil, checkErr
			}
			if !allowed {
				out.Missing = append(out.Missing, resource.name+":"+verb)
			}
		}
	}
	for _, optional := range []struct{ group, resource, detail string }{
		{"networking.k8s.io", "ingresses", "ingress"},
		{"autoscaling", "horizontalpodautoscalers", "hpa"},
	} {
		ready := true
		for _, verb := range []string{"get", "create", "update", "patch", "delete"} {
			allowed, checkErr := c.allowed(ctx, optional.group, optional.resource, verb)
			if checkErr != nil {
				return nil, checkErr
			}
			ready = ready && allowed
		}
		if optional.detail == "hpa" {
			metrics := &metav1.APIResourceList{}
			metricsErr := c.Kube.Discovery().RESTClient().Get().AbsPath("/apis/metrics.k8s.io/v1beta1").Do(ctx).Into(metrics)
			ready = ready && metricsErr == nil
		}
		out.Details[optional.detail] = "unavailable"
		if ready {
			out.Details[optional.detail] = "ready"
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) allowed(ctx context.Context, group, resource, verb string) (bool, error) {
	review, err := c.Kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
			Namespace: c.Config.Namespace, Group: group, Resource: resource, Verb: verb,
		}},
	}, metav1.CreateOptions{})
	if apierrors.IsUnauthorized(err) {
		return false, ErrRejected
	}
	if apierrors.IsForbidden(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check %s %s permission: %w", verb, resource, err)
	}
	return review.Status.Allowed, nil
}

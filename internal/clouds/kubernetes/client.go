package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/dynamic"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ErrRejected marks authentication refused by the selected cluster.
var ErrRejected = errors.New("the cluster refused the credential")

// Client owns a single explicitly configured cluster/namespace connection.
type Client struct {
	Config *Config
	Kube   kubeclient.Interface
	// Dynamic reaches cert-manager objects, which have no typed client here.
	Dynamic dynamic.Interface
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
	httpClient, err := rest.HTTPClientFor(rc)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes transport: %w", err)
	}
	if cfg.ManagedWorkload {
		httpClient.Transport = &managedTransport{next: httpClient.Transport, journal: cfg.ManagedJournal, scope: cfg.ManagedScope}
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client, err := kubeclient.NewForConfigAndClient(rc, httpClient)
	if err != nil {
		httpClient.CloseIdleConnections()
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfigAndClient(rc, httpClient)
	if err != nil {
		httpClient.CloseIdleConnections()
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	copyConfig := *cfg
	copyConfig.REST, copyConfig.Details = rc, maps.Clone(cfg.Details)
	if copyConfig.Details == nil {
		copyConfig.Details = map[string]string{}
	}
	return &Client{Config: &copyConfig, Kube: client, Dynamic: dynamicClient}, nil
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
	if setupErr := c.discoverManagedSetup(ctx, out); setupErr != nil {
		return nil, setupErr
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
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if storageErr := c.checkPersistentStorage(ctx, out); storageErr != nil {
		return nil, storageErr
	}
	if postgresErr := c.checkPostgresPermissions(ctx, out); postgresErr != nil {
		return nil, postgresErr
	}
	// Routes are optional, so what they need is a detail and never Missing.
	routeClient := *c
	routeConfig := *c.Config
	routeConfig.Details = out.Details
	routeClient.Config = &routeConfig
	routes, err := routeClient.CheckRoutePrerequisites(ctx)
	if err != nil {
		return nil, err
	}
	out.Details["route_prerequisites"] = "ready"
	if !routes.Ready {
		out.Details["route_prerequisites"] = "missing: " + strings.Join(routes.Missing, ", ")
	}
	if err := c.checkJobPermissions(ctx, out); err != nil {
		return nil, err
	}
	if err := c.checkServiceProxyPermissions(ctx, out); err != nil {
		return nil, err
	}
	if err := c.checkAccessPermissions(ctx, out); err != nil {
		return nil, err
	}
	c.checkBuildIsolation(ctx, out)
	if err := c.checkManagedBuildPermissions(ctx, out); err != nil {
		return nil, err
	}
	inv, gpuErr := c.GPUs(ctx)
	if gpuErr != nil {
		return nil, gpuErr
	}
	maps.Copy(out.Details, inv.Details())
	return out, nil
}

func (c *Client) allowed(ctx context.Context, group, resource, verb string) (bool, error) {
	return c.allowedIn(ctx, c.Config.Namespace, group, resource, verb)
}
func (c *Client) allowedIn(ctx context.Context, namespace, group, resource, verb string) (bool, error) {
	base, subresource, _ := strings.Cut(resource, "/")
	review, err := c.Kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
			Namespace: namespace, Group: group, Resource: base, Subresource: subresource, Verb: verb,
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

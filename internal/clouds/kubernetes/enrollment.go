package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/xraph/loomplane/pkg/provider"
)

const RuntimeTokenLifetime = time.Hour
const RuntimeTokenRenewalLead = 10 * time.Minute
const enrollmentLabel = "loomplane.io/attachment"
const projectLabel = "loomplane.io/project"

// EnrollmentState contains identity receipts, never credentials.
type EnrollmentState struct {
	AccessInput       *provider.RuntimeAccessInput `json:"access_input,omitempty"`
	AccessReceipt     *RuntimeAccessReceipt        `json:"access_receipt,omitempty"`
	NamespaceUID      string                       `json:"namespace_uid"`
	ServiceAccountUID string                       `json:"service_account_uid"`
	Objects           map[string]string            `json:"objects"`
	Attempted         map[string]bool              `json:"attempted"`
}
type EnrollmentRequest struct {
	RuntimeAccess                                                        *provider.RuntimeAccessProfile
	OperationID, RequestDigest                                           string
	RequireIngress, RequireTLS                                           bool
	AttachmentID, ProjectID, Namespace, NamespaceUID, ServiceAccountName string
	AdoptNamespace                                                       bool
}

// EnrollmentCheckpoint must fence the lease and revalidate authority before an effect.
type EnrollmentCheckpoint func(context.Context, *EnrollmentState) error

func RuntimeRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"services", "secrets"}, Verbs: []string{"get", "create", "update", "patch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
	}
}
func RuntimeClusterRules(namespace string) []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, ResourceNames: []string{namespace}, Verbs: []string{"get"}},
		{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews"}, Verbs: []string{"create"}},
	}
}

// EnrollRuntime creates only the reviewed namespace and exact runtime RBAC objects.
func EnrollRuntime(ctx context.Context, k kubeclient.Interface, in EnrollmentRequest, state *EnrollmentState, checkpoint EnrollmentCheckpoint) error {
	if state == nil || checkpoint == nil || in.AttachmentID == "" || in.ProjectID == "" || !namespaceRE.MatchString(in.Namespace) || !namespaceRE.MatchString(in.ServiceAccountName) {
		return ErrInvalid
	}
	if state.Objects == nil {
		state.Objects = map[string]string{}
	}
	if state.Attempted == nil {
		state.Attempted = map[string]bool{}
	}
	labels := map[string]string{enrollmentLabel: in.AttachmentID, projectLabel: in.ProjectID}
	ns, err := k.CoreV1().Namespaces().Get(ctx, in.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && !in.AdoptNamespace && state.NamespaceUID == "" {
		state.Attempted["Namespace"] = true
		if checkErr := checkpoint(ctx, state); checkErr != nil {
			return checkErr
		}
		ns, err = k.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: in.Namespace, Labels: labels}}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if ns.UID == "" || ns.DeletionTimestamp != nil {
		return ErrInvalid
	}
	if in.AdoptNamespace {
		if in.NamespaceUID == "" || string(ns.UID) != in.NamespaceUID {
			return ErrInvalid
		}
	} else if ns.Labels[enrollmentLabel] != in.AttachmentID || ns.Labels[projectLabel] != in.ProjectID || !state.Attempted["Namespace"] {
		return ErrInvalid
	}
	if state.NamespaceUID != "" && state.NamespaceUID != string(ns.UID) {
		return ErrInvalid
	}
	state.NamespaceUID = string(ns.UID)
	if checkErr := checkpoint(ctx, state); checkErr != nil {
		return checkErr
	}
	meta := metav1.ObjectMeta{Name: in.ServiceAccountName, Namespace: in.Namespace, Labels: labels}
	disabled := false
	rules, err := EnrollmentRoleRules(in)
	if err != nil {
		return err
	}
	role := &rbacv1.Role{ObjectMeta: meta, Rules: rules}
	if in.RuntimeAccess != nil {
		if in.OperationID == "" || in.RequestDigest == "" {
			return ErrInvalid
		}
		role.Annotations = map[string]string{RuntimeAccessOperationAnnotation: in.OperationID, RuntimeAccessDigestAnnotation: in.RequestDigest}
	}
	sa := &corev1.ServiceAccount{ObjectMeta: meta, AutomountServiceAccountToken: &disabled}
	binding := &rbacv1.RoleBinding{ObjectMeta: meta, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: meta.Name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: meta.Name, Namespace: in.Namespace}}}
	clusterMeta := metav1.ObjectMeta{Name: "loomplane-" + in.AttachmentID, Labels: labels}
	cr := &rbacv1.ClusterRole{ObjectMeta: clusterMeta, Rules: RuntimeClusterRules(in.Namespace)}
	cb := &rbacv1.ClusterRoleBinding{ObjectMeta: clusterMeta, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: clusterMeta.Name}, Subjects: binding.Subjects}
	objects := []struct {
		kind   string
		want   runtime.Object
		get    func() (runtime.Object, error)
		create func() (runtime.Object, error)
	}{
		{"ServiceAccount", sa, func() (runtime.Object, error) {
			return k.CoreV1().ServiceAccounts(in.Namespace).Get(ctx, meta.Name, metav1.GetOptions{})
		}, func() (runtime.Object, error) {
			return k.CoreV1().ServiceAccounts(in.Namespace).Create(ctx, sa, metav1.CreateOptions{})
		}},
		{"Role", role, func() (runtime.Object, error) {
			return k.RbacV1().Roles(in.Namespace).Get(ctx, meta.Name, metav1.GetOptions{})
		}, func() (runtime.Object, error) {
			return k.RbacV1().Roles(in.Namespace).Create(ctx, role, metav1.CreateOptions{})
		}},
		{"RoleBinding", binding, func() (runtime.Object, error) {
			return k.RbacV1().RoleBindings(in.Namespace).Get(ctx, meta.Name, metav1.GetOptions{})
		}, func() (runtime.Object, error) {
			return k.RbacV1().RoleBindings(in.Namespace).Create(ctx, binding, metav1.CreateOptions{})
		}},
		{"ClusterRole", cr, func() (runtime.Object, error) {
			return k.RbacV1().ClusterRoles().Get(ctx, clusterMeta.Name, metav1.GetOptions{})
		}, func() (runtime.Object, error) {
			return k.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
		}},
		{"ClusterRoleBinding", cb, func() (runtime.Object, error) {
			return k.RbacV1().ClusterRoleBindings().Get(ctx, clusterMeta.Name, metav1.GetOptions{})
		}, func() (runtime.Object, error) {
			return k.RbacV1().ClusterRoleBindings().Create(ctx, cb, metav1.CreateOptions{})
		}},
	}
	for _, item := range objects {
		obj, getErr := item.get()
		if apierrors.IsNotFound(getErr) && state.Objects[item.kind] == "" {
			state.Attempted[item.kind] = true
			// Finish identity reads before the final authority and lease checkpoint.
			serviceAccount := ""
			if state.ServiceAccountUID != "" {
				serviceAccount = in.ServiceAccountName
			}
			if checkErr := VerifyRuntimeIdentity(ctx, k, in.Namespace, state.NamespaceUID, serviceAccount, state.ServiceAccountUID); checkErr != nil {
				return checkErr
			}
			if checkErr := checkpoint(ctx, state); checkErr != nil {
				return checkErr
			}
			obj, getErr = item.create()
		}
		if getErr != nil {
			return getErr
		}
		actual, ok := obj.(metav1.Object)
		if !ok {
			return ErrInvalid
		}
		if actual.GetUID() == "" || actual.GetDeletionTimestamp() != nil || actual.GetLabels()[enrollmentLabel] != in.AttachmentID || actual.GetLabels()[projectLabel] != in.ProjectID || !state.Attempted[item.kind] {
			return ErrInvalid
		}
		if uid := state.Objects[item.kind]; uid != "" && uid != string(actual.GetUID()) {
			return ErrInvalid
		}
		if !sameRuntimeObject(item.want, obj) {
			return ErrInvalid
		}
		state.Objects[item.kind] = string(actual.GetUID())
		if item.kind == "ServiceAccount" {
			state.ServiceAccountUID = string(actual.GetUID())
		}
		if checkErr := checkpoint(ctx, state); checkErr != nil {
			return checkErr
		}
	}
	if in.RuntimeAccess != nil {
		pin := provider.RuntimeAccessInput{AttachmentID: in.AttachmentID, AttachmentRevision: 1, ProjectID: in.ProjectID, Namespace: in.Namespace, NamespaceUID: state.NamespaceUID, ServiceAccountName: in.ServiceAccountName, ServiceAccountUID: state.ServiceAccountUID, RoleName: in.ServiceAccountName, RoleUID: state.Objects["Role"], RoleBindingName: in.ServiceAccountName, RoleBindingUID: state.Objects["RoleBinding"], Profile: *in.RuntimeAccess}
		actual, binding, readErr := ReadRuntimeAccess(ctx, k, pin)
		if readErr != nil {
			return readErr
		}
		if !reflect.DeepEqual(actual.Rules, rules) || actual.Annotations[RuntimeAccessOperationAnnotation] != in.OperationID || actual.Annotations[RuntimeAccessDigestAnnotation] != in.RequestDigest {
			return ErrInvalid
		}
		pin.RoleResourceVersion = actual.ResourceVersion
		pin.RoleRulesDigest = RuntimeAccessDigest(actual.Rules)
		pin.RoleBindingResourceVersion = binding.ResourceVersion
		pin.RoleBindingSubjectsRefDigest = RuntimeBindingDigest(binding)
		state.AccessInput = &pin
		state.AccessReceipt = &RuntimeAccessReceipt{Kind: "enrolled-role", RoleUID: pin.RoleUID, ResourceVersion: actual.ResourceVersion, RulesDigest: pin.RoleRulesDigest, OperationID: in.OperationID, RequestDigest: in.RequestDigest, ObservedAt: time.Now().UTC()}
		if checkErr := checkpoint(ctx, state); checkErr != nil {
			return checkErr
		}
	}
	return nil
}
func sameRuntimeObject(want, got runtime.Object) bool {
	switch w := want.(type) {
	case *corev1.ServiceAccount:
		g, ok := got.(*corev1.ServiceAccount)
		return ok && g.AutomountServiceAccountToken != nil && !*g.AutomountServiceAccountToken && len(g.Secrets) == 0 && len(g.ImagePullSecrets) == 0
	case *rbacv1.Role:
		g, ok := got.(*rbacv1.Role)
		return ok && reflect.DeepEqual(w.Rules, g.Rules)
	case *rbacv1.ClusterRole:
		g, ok := got.(*rbacv1.ClusterRole)
		return ok && g.AggregationRule == nil && reflect.DeepEqual(w.Rules, g.Rules)
	case *rbacv1.RoleBinding:
		g, ok := got.(*rbacv1.RoleBinding)
		return ok && reflect.DeepEqual(w.RoleRef, g.RoleRef) && reflect.DeepEqual(w.Subjects, g.Subjects)
	case *rbacv1.ClusterRoleBinding:
		g, ok := got.(*rbacv1.ClusterRoleBinding)
		return ok && reflect.DeepEqual(w.RoleRef, g.RoleRef) && reflect.DeepEqual(w.Subjects, g.Subjects)
	}
	return false
}
func VerifyRuntimeIdentity(ctx context.Context, k kubeclient.Interface, namespace, namespaceUID, sa, saUID string) error {
	ns, err := k.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if namespaceUID == "" || string(ns.UID) != namespaceUID || ns.DeletionTimestamp != nil {
		return ErrInvalid
	}
	if sa == "" {
		return nil
	}
	account, err := k.CoreV1().ServiceAccounts(namespace).Get(ctx, sa, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if saUID == "" || string(account.UID) != saUID || account.DeletionTimestamp != nil {
		return ErrInvalid
	}
	return nil
}
func RequestRuntimeToken(ctx context.Context, k kubeclient.Interface, namespace, sa string, now time.Time) (string, time.Time, error) {
	seconds := int64(RuntimeTokenLifetime / time.Second)
	token, err := k.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, sa, &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &seconds}}, metav1.CreateOptions{})
	if err != nil {
		return "", time.Time{}, err
	}
	expires := token.Status.ExpirationTimestamp.Time
	if token.Status.Token == "" || !expires.After(now.Add(RuntimeTokenRenewalLead)) || expires.After(now.Add(RuntimeTokenLifetime+time.Minute)) {
		return "", time.Time{}, errors.New("runtime token expiry is outside the supported renewal window")
	}
	return token.Status.Token, expires, nil
}
func RuntimeKubeconfig(endpoint string, ca []byte, namespace, token string) ([]byte, error) {
	return clientcmd.Write(clientapi.Config{Clusters: map[string]*clientapi.Cluster{"runtime": {Server: endpoint, CertificateAuthorityData: ca}}, AuthInfos: map[string]*clientapi.AuthInfo{"runtime": {Token: token}}, Contexts: map[string]*clientapi.Context{"runtime": {Cluster: "runtime", AuthInfo: "runtime", Namespace: namespace}}, CurrentContext: "runtime"})
}

// RetireRuntimeRBAC removes only objects whose UIDs match the enrollment receipts.
func RetireRuntimeRBAC(ctx context.Context, k kubeclient.Interface, in EnrollmentRequest, state EnrollmentState) error {
	name := "loomplane-" + in.AttachmentID
	for _, kind := range []string{"ClusterRoleBinding", "ClusterRole"} {
		uid := types.UID(state.Objects[kind])
		if uid == "" {
			continue
		}
		opts := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
		var err error
		if kind == "ClusterRoleBinding" {
			err = k.RbacV1().ClusterRoleBindings().Delete(ctx, name, opts)
		} else {
			err = k.RbacV1().ClusterRoles().Delete(ctx, name, opts)
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	for _, kind := range []string{"RoleBinding", "Role", "ServiceAccount"} {
		uid := types.UID(state.Objects[kind])
		if uid == "" {
			continue
		}
		opts := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
		var deleteErr error
		switch kind {
		case "RoleBinding":
			deleteErr = k.RbacV1().RoleBindings(in.Namespace).Delete(ctx, in.ServiceAccountName, opts)
		case "Role":
			deleteErr = k.RbacV1().Roles(in.Namespace).Delete(ctx, in.ServiceAccountName, opts)
		case "ServiceAccount":
			deleteErr = k.CoreV1().ServiceAccounts(in.Namespace).Delete(ctx, in.ServiceAccountName, opts)
		}
		if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			return deleteErr
		}
	}
	return nil
}

// CheckRuntimeBoundaries rejects a runtime identity with inventory or RBAC authority.
func (c *Client) CheckRuntimeBoundaries(ctx context.Context) error {
	other := "default"
	if c.Config.Namespace == other {
		other = "kube-system"
	}
	for _, check := range []struct{ namespace, group, resource, verb string }{
		{"", "", "namespaces", "list"}, {"", "", "nodes", "list"}, {"", "", "persistentvolumes", "get"},
		{"", "rbac.authorization.k8s.io", "clusterrolebindings", "create"},
		{c.Config.Namespace, "rbac.authorization.k8s.io", "rolebindings", "create"},
		{other, "", "secrets", "get"}, {other, "apps", "deployments", "create"},
		{c.Config.Namespace, "", "pods", "create"}, {c.Config.Namespace, "", "pods/exec", "create"},
		{c.Config.Namespace, "apps", "replicasets", "create"}, {c.Config.Namespace, "apps", "replicasets", "update"}, {c.Config.Namespace, "apps", "replicasets", "patch"},
	} {
		allowed, err := c.allowedIn(ctx, check.namespace, check.group, check.resource, check.verb)
		if err != nil {
			return err
		}
		if allowed {
			return errors.New("runtime service account has authority beyond the managed namespace contract")
		}
	}
	return nil
}

// CheckEnrollmentPrerequisites checks existing addons using bootstrap inventory access.
func (c *Client) CheckEnrollmentPrerequisites(ctx context.Context, ingress, tls bool) error {
	if !ingress && !tls {
		return nil
	}
	if c.Config.Details["ingress_address"] == "" {
		return errors.New("selected ingress needs an address")
	}
	classes, err := c.Kube.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	found := false
	for _, class := range classes.Items {
		if class.Spec.Controller != "" {
			found = true
		}
	}
	if !found {
		return errors.New("selected ingress has no existing controller class")
	}
	if tls {
		prerequisites, checkErr := c.CheckRoutePrerequisites(ctx)
		if checkErr != nil {
			return checkErr
		}
		if !prerequisites.Ready {
			return errors.New("selected TLS prerequisites are unavailable")
		}
	}
	return nil
}

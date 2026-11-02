package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/clouds/owned"
	domain "github.com/xraph/loomplane/pkg/federation"
)

const (
	groupIngressLabel     = "loomplane.io/placement-group"
	groupMemberLabel      = "loomplane.io/placement-member"
	groupGenerationLabel  = "loomplane.io/placement-generation"
	groupSwitchAnnotation = "loomplane.io/member-switch"
)

// GroupIngress switches a stable member ingress after private preparation.
// Guard reloads accepted actor, member, attempt and capacity authority before
// mutation. The installed client also journals every HTTP write.
type GroupIngress struct {
	Group  domain.QualifiedGroupPlan
	Client *Client
	Guard  func(context.Context, domain.QualifiedGroupPlan, domain.MemberReceipt) error
	// Recover verifies a matching persisted issuance, including the exact old
	// and new backend and resourceVersion, before accepting an observed switch.
	Recover func(context.Context, domain.QualifiedGroupPlan, domain.ServingMemberReceipt, domain.ServingMemberReceipt) error
	Now     func() time.Time
}

func GroupIngressName(scope domain.Scope, group, member string) string {
	raw, err := json.Marshal([]string{scope.OrgID, scope.ProjectID, scope.EnvironmentID, group, member})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "lp-group-" + hex.EncodeToString(sum[:20])
}

func groupMemberLabels(r domain.MemberReceipt) map[string]string {
	return map[string]string{owned.LabelOrg: r.OrgID, owned.LabelProject: r.ProjectID, owned.LabelEnvironment: r.EnvironmentID, owned.LabelInstance: r.Identity.InstanceID}
}

func groupIngressLabels(r domain.MemberReceipt) map[string]string {
	return map[string]string{owned.LabelOrg: r.OrgID, owned.LabelProject: r.ProjectID, owned.LabelEnvironment: r.EnvironmentID, groupIngressLabel: r.Identity.GroupID, groupMemberLabel: r.Identity.MemberID}
}

func (g *GroupIngress) privateService(ctx context.Context, r domain.MemberReceipt) error {
	svc, err := g.Client.Kube.CoreV1().Services(r.Namespace).Get(ctx, r.PrivateService.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(svc.UID) != r.PrivateService.UID || svc.DeletionTimestamp != nil || svc.Spec.Type != "ClusterIP" || svc.Spec.ExternalName != "" {
		return ErrOwned
	}
	for key, value := range groupMemberLabels(r) {
		if value == "" || svc.Labels[key] != value {
			return ErrOwned
		}
	}
	for _, port := range svc.Spec.Ports {
		if port.Port == r.ServicePort {
			return nil
		}
	}
	return ErrOwned
}

// Promote reconciles an uncertain accepted switch before attempting another
// write. A different backend, UID, owner or resourceVersion requires review.
func (g *GroupIngress) Promote(ctx context.Context, r domain.MemberReceipt, expected domain.ServingMemberReceipt, hostname, tlsSecret, ingressClass string) (domain.ServingMemberReceipt, error) {
	var empty domain.ServingMemberReceipt
	identity, err := domain.NewMemberIdentity(r.Identity.GroupID, r.Identity.Generation, r.Identity.MemberID)
	if err != nil || identity != r.Identity || r.SchemaVersion != 1 || r.OrgID == "" || r.ProjectID == "" || r.EnvironmentID == "" || r.NamespaceUID == "" || r.PrivateService.Kind != "Service" || r.PrivateService.UID == "" || r.PrivateService.ResourceVersion == "" || r.ServicePort < 1 || r.ServicePort > 65535 || len(validation.IsDNS1123Subdomain(hostname)) > 0 || len(validation.IsDNS1123Subdomain(tlsSecret)) > 0 || tlsSecret == "" || len(validation.IsDNS1123Subdomain(r.PrivateService.Name)) > 0 {
		return empty, ErrOwned
	}
	var member *domain.GroupMemberPlan
	for i := range g.Group.Members {
		if g.Group.Members[i].Identity.MemberID == r.Identity.MemberID {
			member = &g.Group.Members[i]
		}
	}
	if member == nil || g.Group.Scope != r.Scope || g.Group.GroupID != r.Identity.GroupID || g.Group.Generation < r.Identity.Generation || g.Group.ConfigRevision != r.ConfigRevision || g.Group.Routing.Hostname != hostname || member.Prerequisite.TLSSecretName != tlsSecret || member.ConnectionID != r.ConnectionID || member.Prerequisite.NamespaceUID != r.NamespaceUID || member.Prerequisite.Namespace != r.Namespace || r.IngressClass != ingressClass || r.ID == "" || r.Revision < 1 {
		return empty, ErrOwned
	}
	if retained := member.RetainedBackend; retained != nil {
		if retained.Identity != r.Identity || retained.ReceiptID != r.ID || retained.ReceiptRevision != r.Revision || retained.SnapshotID != r.SnapshotID || retained.ConfigRevision != r.ConfigRevision || g.Group.Generation <= r.Identity.Generation {
			return empty, ErrOwned
		}
	} else if member.Identity != r.Identity {
		return empty, ErrOwned
	}
	if g.Client == nil || g.Client.Config == nil || g.Client.Config.Namespace != r.Namespace || g.Guard == nil {
		return empty, ErrManagedAuthority
	}
	if guardErr := g.Guard(ctx, g.Group, r); guardErr != nil {
		return empty, guardErr
	}
	if serviceErr := g.privateService(ctx, r); serviceErr != nil {
		return empty, serviceErr
	}
	name := GroupIngressName(r.Scope, r.Identity.GroupID, r.Identity.MemberID)
	old, err := g.Client.Kube.NetworkingV1().Ingresses(r.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return empty, err
	}
	if apierrors.IsNotFound(err) {
		old = nil
	}
	want := groupIngressObject(r, hostname, tlsSecret, ingressClass)
	want.Labels[groupGenerationLabel] = strconv.FormatInt(g.Group.Generation, 10)
	if old != nil {
		for key, value := range groupIngressLabels(r) {
			if old.Labels[key] != value {
				return empty, ErrOwned
			}
		}
		if old.UID == "" || old.ResourceVersion == "" || old.DeletionTimestamp != nil {
			return empty, ErrOwned
		}
		var saved domain.ServingMemberReceipt
		if json.Unmarshal([]byte(old.Annotations[groupSwitchAnnotation]), &saved) != nil {
			return empty, ErrOwned
		}
		if saved.Scope != r.Scope || saved.GroupID != r.Identity.GroupID || saved.MemberID != r.Identity.MemberID || saved.Namespace != r.Namespace || saved.NamespaceUID != r.NamespaceUID || saved.SchemaVersion != 1 || saved.SwitchedAt.IsZero() {
			return empty, ErrOwned
		}
		// Kubernetes assigns these after transmission. The annotation records the
		// accepted intent, and the readback supplies authoritative object metadata.
		saved.Ingress = ingressObjectIdentity(old)
		if saved.Generation == g.Group.Generation && saved.BackendIdentity == r.Identity && saved.BackendReceiptID == r.ID && saved.BackendReceiptRevision == r.Revision && saved.NewService == r.PrivateService && reflect.DeepEqual(old.Spec, want.Spec) {
			if expected.Ingress.UID == "" && saved.OldService.UID != "" || expected.Ingress.UID != "" && (expected.Ingress.UID != saved.Ingress.UID || expected.Ingress.ResourceVersion != saved.PreviousResourceVersion || expected.NewService != saved.OldService) {
				return empty, ErrOwned
			}
			if g.Recover == nil {
				return empty, ErrManagedAuthority
			}
			if recoverErr := g.Recover(ctx, g.Group, expected, saved); recoverErr != nil {
				return empty, recoverErr
			}
			return saved, nil
		}
		if expected.Ingress.UID == "" || !reflect.DeepEqual(saved, expected) || saved.Generation >= g.Group.Generation || string(old.UID) != expected.Ingress.UID || old.ResourceVersion != expected.Ingress.ResourceVersion || !ingressBackendMatches(old, expected.NewService.Name) {
			return empty, ErrOwned
		}
		oldRoute := want.DeepCopy()
		oldRoute.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = expected.NewService.Name
		if !reflect.DeepEqual(old.Spec, oldRoute.Spec) {
			return empty, ErrOwned
		}
		prior, readErr := g.Client.Kube.CoreV1().Services(r.Namespace).Get(ctx, expected.NewService.Name, metav1.GetOptions{})
		if readErr != nil {
			return empty, readErr
		}
		if string(prior.UID) != expected.NewService.UID || prior.DeletionTimestamp != nil {
			return empty, ErrOwned
		}
		want.UID, want.ResourceVersion = old.UID, old.ResourceVersion
	} else if expected.Ingress.UID != "" {
		return empty, ErrOwned
	}
	now := time.Now().UTC()
	if g.Now != nil {
		now = g.Now().UTC()
	}
	receipt := domain.ServingMemberReceipt{Scope: r.Scope, SchemaVersion: 1, GroupID: r.Identity.GroupID, MemberID: r.Identity.MemberID, Generation: g.Group.Generation, BackendIdentity: r.Identity, BackendReceiptID: r.ID, BackendReceiptRevision: r.Revision, Namespace: r.Namespace, NamespaceUID: r.NamespaceUID, OldService: expected.NewService, NewService: r.PrivateService, PreviousResourceVersion: expected.Ingress.ResourceVersion, SwitchedAt: now}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return empty, err
	}
	want.Annotations = map[string]string{groupSwitchAnnotation: string(raw)}
	if guardErr := g.Guard(ctx, g.Group, r); guardErr != nil {
		return empty, guardErr
	}
	var applied *networkingv1.Ingress
	if old == nil {
		applied, err = g.Client.Kube.NetworkingV1().Ingresses(r.Namespace).Create(ctx, want, metav1.CreateOptions{})
	} else {
		applied, err = g.Client.Kube.NetworkingV1().Ingresses(r.Namespace).Update(ctx, want, metav1.UpdateOptions{})
	}
	if err != nil {
		return empty, err
	}
	if applied == nil || applied.UID == "" || applied.ResourceVersion == "" || applied.Name != want.Name || !reflect.DeepEqual(applied.Spec, want.Spec) || !reflect.DeepEqual(applied.Annotations, want.Annotations) || !reflect.DeepEqual(applied.Labels, want.Labels) || old != nil && applied.UID != old.UID {
		return empty, errors.New("placement group ingress readback changed")
	}
	receipt.Ingress = ingressObjectIdentity(applied)
	return receipt, nil
}

func ingressObjectIdentity(i *networkingv1.Ingress) domain.RuntimeObject {
	return domain.RuntimeObject{Kind: "Ingress", Name: i.Name, UID: string(i.UID), ResourceVersion: i.ResourceVersion, Generation: i.Generation}
}

func ingressBackendMatches(i *networkingv1.Ingress, name string) bool {
	return len(i.Spec.Rules) == 1 && i.Spec.Rules[0].HTTP != nil && len(i.Spec.Rules[0].HTTP.Paths) == 1 && i.Spec.Rules[0].HTTP.Paths[0].Backend.Service != nil && i.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name == name
}

func groupIngressObject(r domain.MemberReceipt, hostname, tlsSecret, class string) *networkingv1.Ingress {
	path := networkingv1.PathTypePrefix
	labels := groupIngressLabels(r)
	labels[groupGenerationLabel] = strconv.FormatInt(r.Identity.Generation, 10)
	i := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: GroupIngressName(r.Scope, r.Identity.GroupID, r.Identity.MemberID), Namespace: r.Namespace, Labels: labels}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{hostname}, SecretName: tlsSecret}}, Rules: []networkingv1.IngressRule{{Host: hostname, IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &path, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: r.PrivateService.Name, Port: networkingv1.ServiceBackendPort{Number: r.ServicePort}}}}}}}}}}}
	if class != "" {
		i.Spec.IngressClassName = &class
	}
	return i
}

package kubernetes

import (
	"context"
	"encoding/json"
	"time"

	kubeclient "k8s.io/client-go/kubernetes"
)

// RenewInstanceRuntimeToken issues a token for an unchanged, fully enrolled
// namespace. The caller owns reviewed maintenance authority, lifecycle draining
// and durable credential publication, including an effective runtime-boundary
// check using the issued identity. Observation readers must not call it.
func RenewInstanceRuntimeToken(ctx context.Context, k kubeclient.Interface, in EnrollmentRequest, state *EnrollmentState, now time.Time, checkpoint func(context.Context) error) (string, time.Time, error) {
	if k == nil || state == nil || checkpoint == nil || now.IsZero() || in.InstancePolicy == nil || in.RuntimeAccess != nil || in.AdoptNamespace || state.AccessInput != nil || state.AccessReceipt != nil {
		return "", time.Time{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	// Keep caller-owned maps and policy pointers out of checkpoint callbacks.
	frozen := struct {
		Request EnrollmentRequest
		State   EnrollmentState
	}{in, *state}
	raw, marshalErr := json.Marshal(frozen)
	if marshalErr != nil {
		return "", time.Time{}, marshalErr
	}
	frozen.Request = EnrollmentRequest{}
	frozen.State = EnrollmentState{}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return "", time.Time{}, err
	}
	in, saved := frozen.Request, &frozen.State
	if saved.NamespaceUID == "" || saved.ServiceAccountUID == "" || !saved.Attempted["Namespace"] || saved.Isolation == nil || saved.Objects["ServiceAccount"] != saved.ServiceAccountUID || (in.NamespaceUID != "" && in.NamespaceUID != saved.NamespaceUID) {
		return "", time.Time{}, ErrInvalid
	}
	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"} {
		if saved.Objects[kind] == "" || !saved.Attempted[kind] {
			return "", time.Time{}, ErrInvalid
		}
	}
	for _, kind := range []string{"ResourceQuota", "LimitRange", "NetworkPolicy"} {
		if saved.Isolation.Objects[kind] == "" || !saved.Isolation.Attempted[kind] {
			return "", time.Time{}, ErrInvalid
		}
	}
	check := func(ctx context.Context) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if checkErr := checkpoint(ctx); checkErr != nil {
			return checkErr
		}
		return ctx.Err()
	}
	if err := check(ctx); err != nil {
		return "", time.Time{}, err
	}
	// Complete receipts make enrollment read-only: absent or changed objects
	// fail verification before any creation path can run.
	if err := EnrollRuntime(ctx, k, in, saved, func(ctx context.Context, _ *EnrollmentState) error { return check(ctx) }); err != nil {
		return "", time.Time{}, err
	}
	if err := check(ctx); err != nil {
		return "", time.Time{}, err
	}
	token, expires, err := RequestRuntimeToken(ctx, k, in.Namespace, in.ServiceAccountName, now)
	if err != nil {
		return "", time.Time{}, err
	}
	// Token issuance can race an external RBAC or isolation change. Recheck
	// the complete enrollment before returning material for publication.
	if err := EnrollRuntime(ctx, k, in, saved, func(ctx context.Context, _ *EnrollmentState) error { return check(ctx) }); err != nil {
		return "", time.Time{}, err
	}
	if err := check(ctx); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

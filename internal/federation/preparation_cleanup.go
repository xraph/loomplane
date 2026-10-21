package federation

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/xraph/loomplane/internal/agentinstall"
)

type preparationOwnershipCapture func(context.Context, string, string) (*Preparation, error)

func cleanupPreparationResources(ctx context.Context, client kubernetes.Interface, r *Preparation, capture preparationOwnershipCapture, guard func(context.Context) error) (*Preparation, error) {
	if client == nil || r == nil || r.State != PreparationRetiring || capture == nil || guard == nil {
		return r, ErrInvalid
	}
	check := func(ctx context.Context) error {
		if err := guard(ctx); err != nil {
			return err
		}
		return verifyPreparationNamespace(ctx, client, r.PreparationIntent)
	}
	if err := check(ctx); err != nil {
		return r, err
	}
	if r.CleanupFrom == PreparationReserved {
		return r, nil
	}
	plan, err := agentinstall.NewKubernetesPlan(r.Namespace, r.ID, r.Image, false)
	if err != nil {
		return r, err
	}
	name := "loomplane-agent-" + r.ID
	// Launch cleanup must finish first. An unrecorded config cannot be validated
	// and must not disappear as a side effect of pod garbage collection.
	if _, err = client.CoreV1().ConfigMaps(r.Namespace).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		if err != nil {
			return r, err
		}
		return r, ErrInUse
	}
	accountUID, podUID := "", ""
	if r.AccountUID == "" {
		account, readErr := client.CoreV1().ServiceAccounts(r.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(readErr) {
			return r, ErrInUse
		}
		if readErr != nil {
			return r, readErr
		}
		if !plan.MatchesPreparedAccount(account, r.NamespaceUID) {
			return r, ErrIdentityChanged
		}
		accountUID = string(account.UID)
	}
	if r.PodUID == "" && r.CleanupFrom == PreparationPodPending {
		pod, readErr := client.CoreV1().Pods(r.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(readErr) {
			return r, ErrInUse
		}
		if readErr != nil {
			return r, readErr
		}
		if !plan.MatchesPreparedPod(pod, r.NamespaceUID) {
			return r, ErrIdentityChanged
		}
		podUID = string(pod.UID)
	}
	if accountUID != "" || podUID != "" {
		if checkErr := check(ctx); checkErr != nil {
			return r, checkErr
		}
		saved, saveErr := capture(ctx, accountUID, podUID)
		if saveErr != nil {
			return r, saveErr
		}
		if saved == nil || saved.PreparationIntent != r.PreparationIntent || saved.State != PreparationRetiring || saved.Generation <= r.Generation || (accountUID != "" && saved.AccountUID != accountUID) || (podUID != "" && saved.PodUID != podUID) {
			return r, ErrConflict
		}
		r = saved
	}
	if r.PodUID != "" {
		if checkErr := check(ctx); checkErr != nil {
			return r, checkErr
		}
		if deleteErr := deleteOwnedPod(ctx, client, r.Namespace, name, r.PodUID, check); deleteErr != nil {
			return r, deleteErr
		}
	}
	if r.AccountUID != "" {
		if deleteErr := deletePreparationAccount(ctx, client, r, check); deleteErr != nil {
			return r, deleteErr
		}
	}
	return r, check(ctx)
}
func deletePreparationAccount(ctx context.Context, client kubernetes.Interface, r *Preparation, guard func(context.Context) error) error {
	name := "loomplane-agent-" + r.ID
	accounts := client.CoreV1().ServiceAccounts(r.Namespace)
	account, err := accounts.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return guard(ctx)
	}
	if err != nil {
		return err
	}
	if string(account.UID) != r.AccountUID {
		return guard(ctx)
	}
	if account.ResourceVersion == "" {
		return ErrUnverified
	}
	// A replacement pod may still use the original account. Retain it until all
	// consumers are gone, including terminating pods. Bound each inspection.
	pods, err := client.CoreV1().Pods(r.Namespace).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return err
	}
	if pods.Continue != "" || len(pods.Items) > 500 {
		return ErrInUse
	}
	for i := range pods.Items {
		if pods.Items[i].Spec.ServiceAccountName == name {
			return ErrInUse
		}
	}
	if checkErr := guard(ctx); checkErr != nil {
		return checkErr
	}
	if account.DeletionTimestamp == nil {
		err = accounts.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr.To(types.UID(r.AccountUID)), ResourceVersion: ptr.To(account.ResourceVersion)}})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	remaining, err := accounts.Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && string(remaining.UID) == r.AccountUID {
		return ErrInUse
	}
	return guard(ctx)
}

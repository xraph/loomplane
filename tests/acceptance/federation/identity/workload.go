package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// qualifyWorkload exercises the application issuer against the same live Unix
// attestor as the observer. Shared selectors are intentional in this fixture:
// exact identity selection must hold even when a socket exposes multiple SVIDs.
func qualifyWorkload(ctx context.Context, issuer *workloadidentity.Admin, binding workloadidentity.Binding, socket string, trust workloadidentity.TrustSource, verifier *workloadidentity.Verifier) (err error) {
	binding.Agent = domain.AgentIdentity{}
	binding.EntryID = "61234567-89ab-cdef-0123-456789abcdef"
	binding.CheckedAt = time.Now()
	application := workloadidentity.WorkloadBinding{Binding: binding, Workload: domain.WorkloadSelector{Scope: binding.Destination.Scope, DestinationID: binding.Destination.ID, ServiceID: "api"}, PruneAfter: time.Now().Add(90 * time.Second).Truncate(time.Second)}
	entry, err := issuer.RegisterWorkload(ctx, application)
	if err != nil {
		return fmt.Errorf("application registration: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, issuer.Revoke(cleanup, binding.EntryID))
	}()
	if entry.ExpiresAt != application.PruneAfter.Unix() {
		return errors.New("SPIRE did not retain application pruning timestamp")
	}
	if _, retryErr := issuer.RegisterWorkload(ctx, application); retryErr != nil {
		return fmt.Errorf("application registration retry: %w", retryErr)
	}
	uri, err := workloadidentity.WorkloadURI("loomplane.test", application.Workload)
	if err != nil {
		return err
	}
	source, err := readySource(ctx, socket, uri)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	svid, err := source.GetX509SVID()
	if err != nil {
		return err
	}
	if svid.Certificates[0].NotAfter.After(time.Now().Add(binding.TTL + time.Second)) {
		return errors.New("SPIRE exceeded the requested application certificate lifetime")
	}
	selected, err := workloadidentity.VerifyWorkloadPeer(trust, svid.Certificates, binding.Destination.OrgID, time.Now())
	if err != nil || selected != application.Workload {
		return errors.Join(errors.New("live application identity mismatch"), err)
	}
	if _, verifyErr := verifier.VerifyPeer(ctx, svid.Certificates, "loomplane.test"); verifyErr == nil {
		return errors.New("application identity authenticated as an observer")
	}
	if _, configErr := source.ClientTLS("spiffe://loomplane.test/loomplane/control/org/fixture"); configErr == nil {
		return errors.New("application identity configured observer transport")
	}
	fmt.Println("PASS scoped application registration, exact SVID selection and observer separation")

	neighbor := application
	neighbor.Binding.EntryID = "71234567-89ab-cdef-0123-456789abcdef"
	neighbor.Binding.SHA256 = strings.Repeat("0", 64)
	neighbor.Workload.ServiceID = "neighbor"
	if _, issueErr := issuer.RegisterWorkload(ctx, neighbor); issueErr != nil {
		return issueErr
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, issuer.Revoke(cleanup, neighbor.Binding.EntryID))
	}()
	neighborURI, err := workloadidentity.WorkloadURI("loomplane.test", neighbor.Workload)
	if err != nil {
		return err
	}
	denial, cancelDenial := context.WithTimeout(ctx, 10*time.Second)
	wrong, deniedErr := workloadidentity.OpenSource(denial, socket, neighborURI)
	cancelDenial()
	if deniedErr == nil {
		return errors.Join(errors.New("neighbor with wrong executable obtained application SVID"), wrong.Close())
	}
	if !strings.Contains(deniedErr.Error(), "missing X509-SVID") {
		return fmt.Errorf("application executable denial unproven: %w", deniedErr)
	}
	if revokeErr := issuer.Revoke(ctx, binding.EntryID); revokeErr != nil {
		return revokeErr
	}
	revoked, cancelRevoked := context.WithTimeout(ctx, 40*time.Second)
	defer cancelRevoked()
	if waitErr := until(revoked, func() bool { _, getErr := source.GetX509SVID(); return getErr != nil }); waitErr != nil {
		return fmt.Errorf("revoked application still has a usable SVID: %w", waitErr)
	}
	fmt.Println("PASS application executable mismatch denied and revoked source loses its SVID")

	// SPIRE documents entry expiry as housekeeping, not a security cutoff.
	// Check the source after that timestamp, then explicitly revoke the entry.
	// Access policy must never derive authority from this pruning metadata.
	expiring := application
	expiring.Binding.EntryID = "81234567-89ab-cdef-0123-456789abcdef"
	expiring.Binding.CheckedAt = time.Now()
	expiring.Workload.ServiceID = "expiring"
	expiring.PruneAfter = time.Now().Add(45 * time.Second).Truncate(time.Second)
	if _, issueErr := issuer.RegisterWorkload(ctx, expiring); issueErr != nil {
		return issueErr
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, issuer.Revoke(cleanup, expiring.Binding.EntryID))
	}()
	expiringURI, err := workloadidentity.WorkloadURI("loomplane.test", expiring.Workload)
	if err != nil {
		return err
	}
	expiringSource, err := readySource(ctx, socket, expiringURI)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, expiringSource.Close()) }()
	if waitErr := until(ctx, func() bool { return !time.Now().Before(expiring.PruneAfter) }); waitErr != nil {
		return waitErr
	}
	if _, getErr := expiringSource.GetX509SVID(); getErr == nil {
		fmt.Println("INFO SPIRE serves a usable application SVID after its pruning timestamp; explicit revocation is required")
	}
	if revokeErr := issuer.Revoke(ctx, expiring.Binding.EntryID); revokeErr != nil {
		return revokeErr
	}
	expired, cancelExpired := context.WithTimeout(ctx, 40*time.Second)
	defer cancelExpired()
	if waitErr := until(expired, func() bool { _, getErr := expiringSource.GetX509SVID(); return getErr != nil }); waitErr != nil {
		return fmt.Errorf("application still usable after explicit revocation: %w", waitErr)
	}
	if _, getErr := source.GetX509SVID(); getErr == nil {
		return errors.New("revoked application identity returned after another registration")
	}
	fmt.Println("PASS explicit application revocation works after the SPIRE pruning timestamp")
	return nil
}

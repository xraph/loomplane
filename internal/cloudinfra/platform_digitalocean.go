package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/pkg/provider"
)

// appPlatformDomains puts hostnames on DigitalOcean App Platform apps.
type appPlatformDomains struct {
	client *digitalocean.Client
}

func newAppPlatformDomains(client *digitalocean.Client) *appPlatformDomains {
	return &appPlatformDomains{client: client}
}

const appNoAddress = "App Platform hasn't given this app an address yet. Deploy it, then add the hostname."

var _ PlatformDomains = (*appPlatformDomains)(nil)

func (a *appPlatformDomains) Records(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, _ string, apex bool) ([]PlatformRecord, error) {
	app, err := a.client.GetApp(ctx, cred.Token, b.ExternalID)
	if err != nil {
		return nil, err
	}
	if apex {
		var out []PlatformRecord
		for _, ip := range digitalocean.AppIngressIPv4 {
			out = append(out, PlatformRecord{Type: "A", Value: ip})
		}
		for _, ip := range digitalocean.AppIngressIPv6 {
			out = append(out, PlatformRecord{Type: "AAAA", Value: ip})
		}
		return out, nil
	}
	host := ingressHost(app.DefaultIngress)
	if host == "" {
		return nil, fmt.Errorf("%w: %s", ErrConflict, appNoAddress)
	}
	return []PlatformRecord{{Type: "CNAME", Value: host}}, nil
}

// ingressHost is the host of a default ingress, without its scheme or path.
func ingressHost(ingress string) string {
	ingress = strings.TrimSpace(ingress)
	if ingress == "" {
		return ""
	}
	if !strings.Contains(ingress, "://") {
		ingress = "https://" + ingress
	}
	u, err := url.Parse(ingress)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (a *appPlatformDomains) Attach(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error {
	app, err := a.client.GetApp(ctx, cred.Token, b.ExternalID)
	if err != nil {
		return err
	}
	if err := appDeploying(app, hostname); err != nil {
		return err
	}
	if hasAppDomain(app.Spec, hostname) {
		return appLiveCommit(app, hostname, "adding", reviewedCommit)
	}
	spec := app.Spec
	spec.Domains = append(append([]digitalocean.AppDomainSpec{}, spec.Domains...), digitalocean.AppDomainSpec{Domain: hostname, Type: digitalocean.AppDomainAlias})
	return a.update(ctx, cred, b, app, spec, hostname, "adding", reviewedCommit)
}

func (a *appPlatformDomains) Detach(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error {
	app, err := a.client.GetApp(ctx, cred.Token, b.ExternalID)
	if digitalocean.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := appDeploying(app, hostname); err != nil {
		return err
	}
	if !hasAppDomain(app.Spec, hostname) {
		return appLiveCommit(app, hostname, "removing", reviewedCommit)
	}
	spec := app.Spec
	spec.Domains = nil
	for _, d := range app.Spec.Domains {
		if !strings.EqualFold(d.Domain, hostname) {
			spec.Domains = append(spec.Domains, d)
		}
	}
	return a.update(ctx, cred, b, app, spec, hostname, "removing", reviewedCommit)
}

// update writes the spec. App Platform may start a deployment for it, which
// has to build the reviewed commit. One of any other commit is cancelled
// before it's live, or rolled back to what ran before once it is, and the
// spec change stays for the service's next deploy to carry. before is the
// app as it was read just ahead of the update, for the deployment to roll
// back to.
func (a *appPlatformDomains) update(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, before *digitalocean.App, spec digitalocean.AppSpec, hostname, verb, reviewedCommit string) error {
	app, err := a.client.UpdateApp(ctx, cred.Token, b.ExternalID, spec)
	if err != nil {
		return err
	}
	if app.PendingDeployment == nil && app.InProgressDeployment == nil {
		return nil
	}
	// An update's deployment queues behind a running one, so with both
	// slots set it's the pending one. Otherwise it's the one running.
	own := app.PendingDeployment
	if own == nil {
		own = app.InProgressDeployment
	}
	_, err = a.client.WaitDeployment(ctx, cred.Token, b.ExternalID, own.ID, reviewedCommit)
	var mismatch *digitalocean.CommitMismatchError
	if !errors.As(err, &mismatch) {
		return err
	}
	refused := appCommitRefusal(verb, hostname)
	live := mismatch.Deployment != nil && mismatch.Deployment.Phase == digitalocean.PhaseActive
	if !live {
		if cancelErr := a.client.CancelDeployment(ctx, cred.Token, b.ExternalID, own.ID); cancelErr != nil {
			return cancelErr
		}
		// A deployment in a late phase can go live before the cancel lands,
		// which App Platform answers as if it had.
		d, getErr := a.client.GetDeployment(ctx, cred.Token, b.ExternalID, own.ID)
		if getErr != nil {
			return getErr
		}
		if d.Phase != digitalocean.PhaseActive {
			return fmt.Errorf("%w: %s", ErrConflict, refused)
		}
	}
	// It went live, so cancelling does nothing: put the app back.
	if before.ActiveDeployment == nil {
		return fmt.Errorf("%w: %s", ErrConflict, "App Platform deployed a newer commit than the reviewed one, and there's nothing to roll the app back to. Redeploy an approved plan.")
	}
	if rollbackErr := a.client.Rollback(ctx, cred.Token, b.ExternalID, before.ActiveDeployment.ID); rollbackErr != nil {
		return fmt.Errorf("rolling back the app after a deployment of a newer commit than the reviewed one: %w", rollbackErr)
	}
	return fmt.Errorf("%w: %s", ErrConflict, refused)
}

func (a *appPlatformDomains) Status(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname string) (PlatformDomainStatus, error) {
	app, err := a.client.GetApp(ctx, cred.Token, b.ExternalID)
	if err != nil {
		return PlatformDomainStatus{}, err
	}
	var live *digitalocean.AppDomain
	for i := range app.Domains {
		if strings.EqualFold(app.Domains[i].Spec.Domain, hostname) {
			live = &app.Domains[i]
		}
	}
	if live == nil {
		// Asked for but not yet reported on, or not asked for at all.
		if !hasAppDomain(app.Spec, hostname) {
			return PlatformDomainStatus{}, nil
		}
		return PlatformDomainStatus{Attached: true, CertState: "issuing"}, nil
	}
	out := PlatformDomainStatus{Attached: true, CertExpires: live.CertificateExpiresAt}
	switch live.Phase {
	case digitalocean.AppDomainActive:
		out.CertState = "ready"
	case digitalocean.AppDomainError:
		out.CertState = "failed"
		out.Message = appDomainFailure(live)
	default:
		out.CertState = "issuing"
	}
	if !hasAppDomain(app.Spec, hostname) {
		out.Attached = false
	}
	return out, nil
}

// appDomainFailure says why a domain is in error: the reason App Platform
// gives for its failing step, or else that step.
func appDomainFailure(d *digitalocean.AppDomain) string {
	if d.Progress != nil {
		for _, step := range d.Progress.Steps {
			if step.Reason != nil && step.Reason.Message != "" {
				return step.Reason.Message
			}
		}
		for _, step := range d.Progress.Steps {
			if strings.EqualFold(step.Status, "ERROR") || strings.EqualFold(step.Status, "FAILED") {
				return "App Platform failed at " + step.Name + " (" + strings.ToLower(step.Status) + ")."
			}
		}
	}
	return "App Platform couldn't set this hostname up."
}

func hasAppDomain(spec digitalocean.AppSpec, hostname string) bool {
	for _, d := range spec.Domains {
		if strings.EqualFold(d.Domain, hostname) {
			return true
		}
	}
	return false
}

// appLiveCommit lets a change with nothing left to do finish only while the
// app's live deployment built the reviewed commit. A wait that was cut short,
// or an update that showed no deployment, may have left another commit live.
// The deployment before it isn't known here, so there's nothing to roll back
// to: the service's next deploy puts the reviewed commit back.
func appLiveCommit(app *digitalocean.App, hostname, verb, reviewedCommit string) error {
	if app.ActiveDeployment == nil {
		return appDeploymentNotice(hostname)
	}
	if !app.ActiveDeployment.Built(reviewedCommit) {
		return fmt.Errorf("%w: %s", ErrConflict, appCommitRefusal(verb, hostname))
	}
	return nil
}

func appCommitRefusal(verb, hostname string) string {
	return "App Platform started a deployment of a newer commit than the reviewed one. Deploy the service to finish " + verb + " " + hostname + "."
}

// appDeploying holds a change to an app's domains until its current
// deployment is done, so it doesn't start another over it.
func appDeploying(app *digitalocean.App, hostname string) error {
	if app.PendingDeployment != nil || app.InProgressDeployment != nil {
		return appDeploymentNotice(hostname)
	}
	return nil
}

func appDeploymentNotice(hostname string) error {
	return &routeNoticeError{err: ErrPending, message: "App Platform is still deploying this app. Loomplane changes " + hostname + " when that deployment finishes."}
}

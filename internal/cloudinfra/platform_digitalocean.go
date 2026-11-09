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

// Refusals the adapter gives. A deployment of another commit is stopped,
// and the service's own deploy puts the reviewed commit back.
const (
	appNoAddress     = "App Platform hasn't given this app an address yet. Deploy it, then add the hostname."
	appCommitRefusal = "App Platform started a deployment of a newer commit than the reviewed one. Deploy the service, then retry the hostname."
)

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
		return nil, platformRefusal(ErrConflict, appNoAddress)
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
	before, app, err := a.change(ctx, cred, b, hostname, func(spec digitalocean.AppSpec) (digitalocean.AppSpec, bool) {
		if hasAppDomain(spec, hostname) {
			return spec, false
		}
		spec.Domains = append(append([]digitalocean.AppDomainSpec{}, spec.Domains...), digitalocean.AppDomainSpec{Domain: hostname, Type: digitalocean.AppDomainAlias})
		return spec, true
	})
	if err != nil {
		return err
	}
	if app == nil {
		return appLiveCommit(before, hostname, reviewedCommit)
	}
	return a.settle(ctx, cred, b, before, app, reviewedCommit)
}

func (a *appPlatformDomains) Detach(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error {
	before, app, err := a.change(ctx, cred, b, hostname, func(spec digitalocean.AppSpec) (digitalocean.AppSpec, bool) {
		if !hasAppDomain(spec, hostname) {
			return spec, false
		}
		domains := spec.Domains
		spec.Domains = nil
		for _, d := range domains {
			if !strings.EqualFold(d.Domain, hostname) {
				spec.Domains = append(spec.Domains, d)
			}
		}
		return spec, true
	})
	if digitalocean.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Gone already: there's nothing to check, and waiting would only
	// strand the hostname's records.
	if app == nil {
		return nil
	}
	return a.settle(ctx, cred, b, before, app, reviewedCommit)
}

// change reads the app and writes back the spec edit makes of it, holding
// the app in between so a deploy can't write over it, nor it over a
// deploy. edit says false when the spec has what it wants already, and
// the app comes back nil. before is the app as it was read. A change
// waits for a deployment that's running, so it doesn't start another
// over it.
func (a *appPlatformDomains) change(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname string, edit func(digitalocean.AppSpec) (digitalocean.AppSpec, bool)) (before, app *digitalocean.App, err error) {
	err = a.client.WithApp(ctx, b.ExternalID, func(ctx context.Context) error {
		var getErr error
		if before, getErr = a.client.GetApp(ctx, cred.Token, b.ExternalID); getErr != nil {
			return getErr
		}
		spec, changed := edit(before.Spec)
		if !changed {
			return nil
		}
		if checkErr := appDeploying(before, hostname); checkErr != nil {
			return checkErr
		}
		var updateErr error
		app, updateErr = a.client.UpdateApp(ctx, cred.Token, b.ExternalID, spec)
		return updateErr
	})
	return before, app, err
}

// settle checks the deployment App Platform may have started for a spec
// change, which has to build the reviewed commit. One of any other commit
// is cancelled before it's live, or rolled back to what ran before once it
// is, and the spec change stays for the service's next deploy to carry.
// before is the app as it was read just ahead of the update, for the
// deployment to roll back to.
func (a *appPlatformDomains) settle(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, before, app *digitalocean.App, reviewedCommit string) error {
	if app.PendingDeployment == nil && app.InProgressDeployment == nil {
		return nil
	}
	// An update's deployment queues behind a running one, so with both
	// slots set it's the pending one. Otherwise it's the one running.
	own := app.PendingDeployment
	if own == nil {
		own = app.InProgressDeployment
	}
	_, err := a.client.WaitDeployment(ctx, cred.Token, b.ExternalID, own.ID, reviewedCommit)
	var mismatch *digitalocean.CommitMismatchError
	if !errors.As(err, &mismatch) {
		return err
	}
	refused := platformRefusal(ErrConflict, appCommitRefusal)
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
			return refused
		}
	}
	// It went live, so cancelling does nothing: put the app back.
	if before.ActiveDeployment == nil {
		return platformRefusal(ErrConflict, "App Platform deployed a newer commit than the reviewed one, and there's nothing to roll the app back to. Deploy the service, then retry the hostname.")
	}
	if _, rollbackErr := a.client.Rollback(ctx, cred.Token, b.ExternalID, before.ActiveDeployment.ID); rollbackErr != nil {
		return fmt.Errorf("rolling back the app after a deployment of a newer commit than the reviewed one: %w", rollbackErr)
	}
	return refused
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
	out := PlatformDomainStatus{Attached: true, CertExpires: live.CertificateExpiresAt.Time}
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

// appLiveCommit lets an attach with nothing left to change finish only
// while the app's live deployment built the reviewed commit. A wait that was
// cut short, or an update that showed no deployment, may have left another
// commit live, and so does a deploy of the service that hasn't recorded its
// commit yet. Either way the service's next deploy settles it, so it waits.
// Without a reviewed commit there's nothing to check.
func appLiveCommit(app *digitalocean.App, hostname, reviewedCommit string) error {
	if reviewedCommit == "" {
		return nil
	}
	if err := appDeploying(app, hostname); err != nil {
		return err
	}
	if app.ActiveDeployment == nil {
		return appDeploymentNotice(hostname)
	}
	if !app.ActiveDeployment.Built(reviewedCommit) {
		return &routeNoticeError{err: ErrPending, message: "App Platform is running a different commit than the reviewed one. Loomplane checks again once this service's deploy finishes. If none is running, deploy the service."}
	}
	return nil
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

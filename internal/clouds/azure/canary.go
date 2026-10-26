package azure

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
)

// A container app's revisions modes. Loomplane runs an app in single
// revision mode, and switches it to multiple revisions mode while a canary
// splits its traffic between two revisions.
const (
	SingleRevision    = "Single"
	MultipleRevisions = "Multiple"
)

// CanaryActions are what a service principal needs, beyond Actions, for
// Loomplane to split a container app's traffic for a canary: reading and
// deactivating its revisions, and listing its secrets, which the
// candidate's PUT has to send back with their values. Contributor has
// them. They aren't needed for anything else, so a principal without them
// still deploys.
var CanaryActions = []string{
	"Microsoft.App/containerApps/revisions/read",
	"Microsoft.App/containerApps/revisions/deactivate/action",
	"Microsoft.App/containerApps/listSecrets/action",
}

// TrafficWeight is one revision's share of an app's traffic: a revision
// by name, or whichever revision is the latest.
type TrafficWeight struct {
	RevisionName   string `json:"revisionName,omitempty"`
	Weight         int    `json:"weight"`
	LatestRevision bool   `json:"latestRevision,omitempty"`
}

// Latest sends all of an app's traffic to its latest revision.
func Latest() []TrafficWeight { return []TrafficWeight{{LatestRevision: true, Weight: 100}} }

// CheckTraffic refuses a split Container Apps would refuse, or that can't
// be meant: one that doesn't add up to 100, a weight outside 0 to 100, an
// entry that names both a revision and the latest or neither, and a
// revision named twice.
func CheckTraffic(traffic []TrafficWeight) error {
	if len(traffic) == 0 {
		return errors.New("a traffic split needs at least one revision")
	}
	total, latest := 0, 0
	seen := map[string]bool{}
	for _, t := range traffic {
		switch {
		case t.Weight < 0 || t.Weight > 100:
			return fmt.Errorf("a revision can't take %d%% of the traffic", t.Weight)
		case t.LatestRevision == (t.RevisionName != ""):
			return errors.New("each share of the traffic names a revision or the latest one, not both or neither")
		case t.LatestRevision:
			latest++
		case seen[strings.ToLower(t.RevisionName)]:
			return fmt.Errorf("revision %s is named twice in the traffic", t.RevisionName)
		default:
			seen[strings.ToLower(t.RevisionName)] = true
		}
		total += t.Weight
	}
	if latest > 1 {
		return errors.New("the latest revision is named twice in the traffic")
	}
	if total != 100 {
		return fmt.Errorf("the traffic adds up to %d%%, not 100%%", total)
	}
	return nil
}

// Revision is a container app revision: whether it runs, what share of the
// traffic it has, and the template it was made from.
type Revision struct {
	Name       string             `json:"name"`
	Properties RevisionProperties `json:"properties"`
}

// RevisionProperties are a revision's. All of them are read only.
type RevisionProperties struct {
	Active              bool     `json:"active"`
	TrafficWeight       int      `json:"trafficWeight"`
	ProvisioningState   string   `json:"provisioningState"`
	ProvisioningError   string   `json:"provisioningError,omitempty"`
	RunningState        string   `json:"runningState"`
	RunningStateDetails string   `json:"runningStateDetails,omitempty"`
	HealthState         string   `json:"healthState"`
	Template            Template `json:"template"`
}

// downRunningStates are running states of a revision that can't serve.
var downRunningStates = []string{"failed", "stopped", "degraded", "deprovisioning", "deprovisioned"}

// Up reports whether a revision can take traffic: it's there, active,
// provisioned, and neither failed nor unhealthy. An active revision that
// scaled to zero is up: it starts a replica when traffic comes.
func (r *Revision) Up() bool { return r.Why() == "" }

// Why says why a revision can't take traffic, or "" when it can.
func (r *Revision) Why() string {
	if r == nil {
		return "it's gone"
	}
	p := r.Properties
	detail := func(state, more string) string {
		why := "it's " + strings.ToLower(state)
		if more = strings.TrimSpace(more); more != "" {
			why += ": " + more
		}
		return why
	}
	switch running := strings.ToLower(p.RunningState); {
	case !p.Active:
		return "it isn't active"
	case !strings.EqualFold(p.ProvisioningState, "Provisioned"):
		return detail(cmp.Or(p.ProvisioningState, "not provisioned"), p.ProvisioningError)
	case slices.Contains(downRunningStates, running):
		return detail(p.RunningState, p.RunningStateDetails)
	case strings.EqualFold(p.HealthState, "Unhealthy"):
		return detail("unhealthy", p.RunningStateDetails)
	}
	return ""
}

// Runs reports whether a revision runs t: the same containers, each with
// the same image, variables (values and the secrets they read), CPU and
// memory, and the same scale. Container Apps keeps secrets on the app, so
// a secret is told apart by its name. The revision suffix isn't part of
// it. Anything Container Apps fills in that t leaves out reads as a
// difference, which is the safe side.
func (r *Revision) Runs(t Template) bool {
	if r == nil {
		return false
	}
	have := r.Properties.Template
	if len(have.Containers) != len(t.Containers) || !sameScale(have.Scale, t.Scale) {
		return false
	}
	for i := range t.Containers {
		if !sameContainer(have.Containers[i], t.Containers[i]) {
			return false
		}
	}
	return true
}

func sameScale(a, b *Scale) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameContainer(a, b Container) bool {
	if a.Name != b.Name || a.Image != b.Image {
		return false
	}
	if (a.Resources == nil) != (b.Resources == nil) || (a.Resources != nil && *a.Resources != *b.Resources) {
		return false
	}
	env := func(c Container) map[string]EnvVar {
		out := make(map[string]EnvVar, len(c.Env))
		for _, e := range c.Env {
			out[e.Name] = e
		}
		return out
	}
	return len(a.Env) == len(b.Env) && maps.Equal(env(a), env(b))
}

// revisionPath is the resource ID of a revision of the app appID.
func (s *Session) revisionPath(appID, revision string) (string, error) {
	if !s.Owns(appID) {
		return "", fmt.Errorf("%s isn't a container app in subscription %s", appID, s.cred.SubscriptionID)
	}
	if revision == "" || strings.ContainsAny(revision, "/?#") {
		return "", fmt.Errorf("%q isn't a revision name", revision)
	}
	return appID + "/revisions/" + url.PathEscape(revision), nil
}

// GetRevision reads one of an app's revisions; a missing one is
// IsNotFound.
func (s *Session) GetRevision(ctx context.Context, appID, revision string) (*Revision, error) {
	p, err := s.revisionPath(appID, revision)
	if err != nil {
		return nil, err
	}
	var rev Revision
	if _, err := s.do(ctx, http.MethodGet, p, appsVersion, nil, &rev); err != nil {
		return nil, err
	}
	if rev.Name == "" {
		rev.Name = revision
	}
	return &rev, nil
}

// Revisions lists an app's revisions, active or not.
func (s *Session) Revisions(ctx context.Context, appID string) ([]Revision, error) {
	if !s.Owns(appID) {
		return nil, fmt.Errorf("%s isn't a container app in subscription %s", appID, s.cred.SubscriptionID)
	}
	var all []Revision
	next, version := appID+"/revisions", appsVersion
	for next != "" {
		var page struct {
			Value    []Revision `json:"value"`
			NextLink string     `json:"nextLink"`
		}
		if _, err := s.do(ctx, http.MethodGet, next, version, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		// The next link carries its own api-version.
		next, version = page.NextLink, ""
	}
	return all, nil
}

// ActiveRevisions lists an app's active revisions.
func (s *Session) ActiveRevisions(ctx context.Context, appID string) ([]Revision, error) {
	all, err := s.Revisions(ctx, appID)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(r Revision) bool { return !r.Properties.Active }), nil
}

// DeactivateRevision deactivates one of an app's revisions and waits until
// it's inactive. One that's inactive already, or gone, is left as it is.
// Azure refuses to deactivate a revision that takes traffic.
func (s *Session) DeactivateRevision(ctx context.Context, appID, revision string) error {
	p, err := s.revisionPath(appID, revision)
	if err != nil {
		return err
	}
	for asked := false; ; {
		rev, gerr := s.GetRevision(ctx, appID, revision)
		switch {
		case IsNotFound(gerr):
			return nil
		case gerr != nil:
			return gerr
		case !rev.Properties.Active:
			return nil
		case !asked:
			if _, derr := s.do(ctx, http.MethodPost, p+"/deactivate", appsVersion, nil, nil); derr != nil && !IsNotFound(derr) {
				return fmt.Errorf("deactivate revision %s: %w", revision, derr)
			}
			asked = true
			continue
		}
		progress(ctx, "Deactivating revision "+revision)
		if werr := s.wait(ctx); werr != nil {
			return werr
		}
	}
}

// ListSecrets reads an app's secrets with their values.
func (s *Session) ListSecrets(ctx context.Context, appID string) ([]Secret, error) {
	if !s.Owns(appID) {
		return nil, fmt.Errorf("%s isn't a container app in subscription %s", appID, s.cred.SubscriptionID)
	}
	var out struct {
		Value []Secret `json:"value"`
	}
	if _, err := s.do(ctx, http.MethodPost, appID+"/listSecrets", appsVersion, nil, &out); err != nil {
		return nil, fmt.Errorf("list the container app's secrets: %w", err)
	}
	return out.Value, nil
}

// SetTraffic sets an app's revisions mode and splits its traffic. It
// PATCHes the app's ingress, as GET returns it with only the traffic
// replaced, and the mode, and nothing else: no template, so no revision is
// made, and no secrets, whose values GET never returns. Then it waits for
// the app to settle and reads the split back. Container Apps can report an
// app whose newest revision won't run as failed after any change, so the
// split in place is what counts, not how the PATCH went.
func (s *Session) SetTraffic(ctx context.Context, appID, mode string, traffic []TrafficWeight) error {
	if err := CheckTraffic(traffic); err != nil {
		return err
	}
	switch mode {
	case MultipleRevisions:
	case SingleRevision:
		if slices.ContainsFunc(traffic, func(t TrafficWeight) bool { return t.RevisionName != "" }) {
			return errors.New("in single revision mode all of the traffic goes to the latest revision")
		}
	default:
		return fmt.Errorf("%q isn't a revisions mode", mode)
	}
	if !s.Owns(appID) {
		return fmt.Errorf("%s isn't a container app in subscription %s", appID, s.cred.SubscriptionID)
	}
	var raw struct {
		Properties struct {
			Configuration struct {
				Ingress map[string]any `json:"ingress"`
			} `json:"configuration"`
		} `json:"properties"`
	}
	if _, err := s.do(ctx, http.MethodGet, appID, appsVersion, nil, &raw); err != nil {
		return err
	}
	ingress := raw.Properties.Configuration.Ingress
	if ingress == nil {
		return fmt.Errorf("container app %s takes no traffic, so it has none to split", path.Base(appID))
	}
	delete(ingress, "fqdn") // read only
	ingress["traffic"] = traffic
	body := map[string]any{"properties": map[string]any{"configuration": map[string]any{
		"activeRevisionsMode": mode, "ingress": ingress,
	}}}
	_, perr := s.do(ctx, http.MethodPatch, appID, appsVersion, body, nil)
	app, werr := s.settle(ctx, appID)
	switch {
	case werr != nil && perr != nil:
		return fmt.Errorf("split the traffic: %w", perr)
	case werr != nil:
		return werr
	case trafficIs(app, mode, traffic):
		return nil
	case perr != nil:
		return fmt.Errorf("split the traffic: %w", perr)
	}
	return fmt.Errorf("azure didn't split container app %s's traffic as asked; its provisioning is %s", path.Base(appID), strings.ToLower(app.Properties.ProvisioningState))
}

// trafficIs reports whether app is in mode with its traffic split as
// traffic says. Shares of nothing don't count.
func trafficIs(app *App, mode string, traffic []TrafficWeight) bool {
	have := cmp.Or(app.Properties.Configuration.ActiveRevisionsMode, SingleRevision)
	if !strings.EqualFold(have, mode) || app.Properties.Configuration.Ingress == nil {
		return false
	}
	return maps.Equal(shares(app.Properties.Configuration.Ingress.Traffic), shares(traffic))
}

// shares is a split by revision, "" for the latest, without the zeros.
func shares(traffic []TrafficWeight) map[string]int {
	out := map[string]int{}
	for _, t := range traffic {
		key := strings.ToLower(t.RevisionName)
		if t.LatestRevision {
			key = ""
		}
		if t.Weight > 0 {
			out[key] += t.Weight
		}
	}
	return out
}

// RestoreTemplate makes an app's template the one its revision called
// revision was made from, under a new revision suffix, which Container
// Apps needs to make it a revision again. It PATCHes only the template,
// with what GET returns of the revision's, so nothing Loomplane doesn't
// model is lost. The new revision takes whatever traffic the latest
// revision is given: none, while the traffic names revisions.
func (s *Session) RestoreTemplate(ctx context.Context, appID, revision, suffix string) error {
	p, err := s.revisionPath(appID, revision)
	if err != nil {
		return err
	}
	var data json.RawMessage
	if _, err := s.do(ctx, http.MethodGet, p, appsVersion, nil, &data); err != nil {
		return err
	}
	var raw struct {
		Properties struct {
			Template map[string]any `json:"template"`
		} `json:"properties"`
	}
	var source Revision
	if err := errors.Join(json.Unmarshal(data, &raw), json.Unmarshal(data, &source)); err != nil {
		return fmt.Errorf("azure: decode revision %s: %w", revision, err)
	}
	tmpl := raw.Properties.Template
	if tmpl == nil {
		return fmt.Errorf("azure shows no template for revision %s", revision)
	}
	tmpl["revisionSuffix"] = suffix
	_, perr := s.do(ctx, http.MethodPatch, appID, appsVersion, map[string]any{"properties": map[string]any{"template": tmpl}}, nil)
	app, werr := s.settle(ctx, appID)
	switch {
	case werr != nil && perr != nil:
		return fmt.Errorf("put revision %s's template back: %w", revision, perr)
	case werr != nil:
		return werr
	case app.Properties.Template.RevisionSuffix == suffix && (&Revision{Properties: RevisionProperties{Template: app.Properties.Template}}).Runs(source.Properties.Template):
		return nil
	case perr != nil:
		return fmt.Errorf("put revision %s's template back: %w", revision, perr)
	}
	return fmt.Errorf("azure didn't put revision %s's template back on container app %s", revision, path.Base(appID))
}

// settle waits until an app's provisioning has finished, either way, and
// returns it.
func (s *Session) settle(ctx context.Context, appID string) (*App, error) {
	for {
		app, err := s.GetApp(ctx, appID)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(app.Properties.ProvisioningState) {
		case "succeeded", "failed", "canceled":
			return app, nil
		}
		progress(ctx, "Waiting for the container app to settle")
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
	}
}

// MissingOf returns which of actions the principal lacks in the resource
// group, or in the subscription while the group isn't there, in the order
// actions lists them.
func (s *Session) MissingOf(ctx context.Context, actions []string) ([]string, error) {
	scope, _, err := s.checkScope(ctx)
	if err != nil {
		return nil, err
	}
	perms, err := s.permissionsOrNone(ctx, scope)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, action := range actions {
		if !allowed(perms, action) {
			missing = append(missing, action)
		}
	}
	return missing, nil
}

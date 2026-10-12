package connections

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/vercel"
)

// GCPChecker checks Google Cloud service account keys.
type GCPChecker struct{ Client *gcp.Client }

// Parse implements Checker.
func (g GCPChecker) Parse(credential []byte, _ map[string]string) (Check, error) {
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return Check{
		Account: key.ProjectID,
		Details: map[string]string{"project_id": key.ProjectID, "service_account": key.ClientEmail},
	}, nil
}

// Check implements Checker: it asks Google which of the permissions a
// deployment needs the service account holds.
func (g GCPChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := g.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return Check{}, err
	}
	s, err := g.Client.Session(ctx, key)
	if err != nil {
		return Check{}, err
	}
	if c.Missing, err = s.Missing(ctx); err != nil {
		return Check{}, err
	}
	return c, nil
}

// teamRE matches Vercel team IDs, such as team_a1B2c3.
var teamRE = regexp.MustCompile(`^team_[A-Za-z0-9]{1,64}$`)

// VercelChecker checks Vercel access tokens.
type VercelChecker struct{ Client *vercel.Client }

// Parse implements Checker. The one setting is team_id; without it the
// token deploys to the person's own account.
func (v VercelChecker) Parse(credential []byte, settings map[string]string) (Check, error) {
	if err := vercel.CheckToken(string(credential)); err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	details := map[string]string{}
	if team := strings.TrimSpace(settings["team_id"]); team != "" {
		if !teamRE.MatchString(team) {
			return Check{}, fmt.Errorf("%w: %q isn't a Vercel team ID; they look like team_ followed by letters and digits", ErrInvalid, team)
		}
		details["team_id"] = team
	}
	return Check{Details: details}, nil
}

// Check implements Checker: it asks Vercel who the token is.
func (v VercelChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := v.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	acct, err := v.Client.Check(ctx, vercel.Token{Value: strings.TrimSpace(string(credential)), TeamID: c.Details["team_id"]})
	if err != nil {
		return Check{}, err
	}
	c.Account = acct.Name
	return c, nil
}

// rejected reports whether a check failed because the cloud refused the
// credential, as opposed to the cloud being unreachable.
func rejected(err error) bool {
	var ge *gcp.APIError
	var ve *vercel.APIError
	return errors.Is(err, gcp.ErrRejected) || errors.Is(err, gcp.ErrInvalidKey) || errors.Is(err, vercel.ErrRejected) ||
		errors.As(err, &ge) || errors.As(err, &ve)
}

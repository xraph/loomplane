package fabriqloom

import (
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/recipes"
)

const (
	controlTokenSecret = "admin-control-token"
	// #nosec G101 -- names a variable, holds no credential
	controlTokenEnv = "FABRIQ_ADMIN_CONTROL_TOKEN"
	// #nosec G101 -- an HTTP header's name, not a credential
	controlTokenHeader = "X-Fabriq-Control-Token"
	legacyVersion      = "1.9"
	legacyRevision     = "1"
)

var (
	controlTokenShape    = regexp.MustCompile("^[0-9a-f]{64}$")
	errControlCredential = errors.New("Fabriq's control credential is missing or invalid") //nolint:staticcheck // ST1005: Fabriq is a name
)

type controlCredential struct {
	token  string
	legacy bool
}

func (c controlCredential) valid() bool { return c.token != "" || c.legacy }

func validateControlToken(value []byte) (controlCredential, error) {
	if !controlTokenShape.Match(value) {
		return controlCredential{}, errControlCredential
	}
	return controlCredential{token: string(value)}, nil
}

func resolveControlCredential(read func(name string) ([]byte, error)) (controlCredential, error) {
	if read == nil {
		return controlCredential{}, errControlCredential
	}
	value, err := read(controlTokenSecret)
	if err == nil {
		return validateControlToken(value)
	}
	// CatalogAccess returns this type directly only when the scoped parent
	// exists and its declaration omits the secret.
	undeclared, ok := err.(*deployments.GeneratedSecretNotDeclaredError) //nolint:errorlint // wrapped source failures must not select legacy mode
	if value == nil && ok && undeclared != nil &&
		undeclared.Kind == "fabriq" && undeclared.Version == legacyVersion &&
		undeclared.Revision == legacyRevision && undeclared.Name == controlTokenSecret {
		return controlCredential{legacy: true}, nil
	}
	return controlCredential{}, redactedError{msg: "couldn't read Fabriq's control credential", err: err}
}

// runControlCredential uses the pending component's rendered inputs. Its
// parent binding may still describe the release this component replaces.
func runControlCredential(run deployments.ComponentRun) (controlCredential, error) {
	declared := false
	for _, env := range run.Component.Env {
		if env.Name != controlTokenEnv {
			continue
		}
		if declared || env != (recipes.EnvVar{Name: controlTokenEnv, Secret: controlTokenSecret}) {
			return controlCredential{}, errControlCredential
		}
		declared = true
	}
	if declared {
		credential, err := validateControlToken(run.Secrets[controlTokenSecret])
		if err != nil || credential.token != run.Env[controlTokenEnv] {
			return controlCredential{}, errControlCredential
		}
		return credential, nil
	}
	if !isLegacyAdminRun(run) {
		return controlCredential{}, errControlCredential
	}
	return controlCredential{legacy: true}, nil
}

func isLegacyAdminRun(run deployments.ComponentRun) bool {
	if run.Kind != "fabriq" {
		return false
	}
	if _, present := run.Secrets[controlTokenSecret]; present {
		return false
	}
	// The deployment pins the architecture in the image reference. Match
	// the image and component definition from that historical recipe.
	for _, arch := range []string{"amd64", "arm64"} {
		_, historical, err := recipes.Default().ResolveRevision("fabriq", legacyVersion, legacyRevision, arch, run.Provider)
		if err != nil {
			return false
		}
		for _, component := range historical.Components {
			if component.Name == consoleComponent && component.Image == run.Component.Image {
				return reflect.DeepEqual(component.Component, run.Component.Component) && legacyAdminEnvMatches(component.Env, run)
			}
		}
	}
	return false
}

func legacyAdminEnvMatches(env []recipes.EnvVar, run deployments.ComponentRun) bool {
	present := 0
	for _, v := range env {
		got, ok := run.Env[v.Name]
		if !ok && v.Requires != "" {
			continue
		}
		present++
		switch {
		case v.Secret != "":
			if got == "" || got != string(run.Secrets[v.Secret]) {
				return false
			}
		case v.From != nil:
			if got == "" {
				return false
			}
		case !ok || got != v.Value:
			return false
		}
	}
	return len(run.Env) == present
}

func newAdminClient(ch *deployments.ComponentChannel, resolve func() (controlCredential, error)) (*AdminClient, func(), error) {
	closeChannel := func() {
		if ch != nil && ch.Close != nil {
			_ = ch.Close() //nolint:errcheck // the channel is abandoned; there is nothing left to retry
		}
	}
	credential, err := resolve()
	if err != nil {
		closeChannel()
		return nil, nil, err
	}
	return &AdminClient{channel: ch, credential: credential}, closeChannel, nil
}

func (c controlCredential) redactErr(err error) error {
	if c.token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.token, "[redacted]")
	if msg == err.Error() {
		return err
	}
	return redactedError{msg: msg, err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }

package deployments

import "errors"

// finishedError is a failure the provider reported as final: the
// deployment it was building ended failed or cancelled, so nothing the
// attempt started is still running there. Anything else, such as a lost
// answer while it builds, leaves the outcome unknown.
type finishedError struct{ err error }

func (e *finishedError) Error() string { return e.err.Error() }
func (e *finishedError) Unwrap() error { return e.err }

// finishedByProvider reports whether err is the provider's final answer.
func finishedByProvider(err error) bool {
	var f *finishedError
	return errors.As(err, &f)
}

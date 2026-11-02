package deployments

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/realtime"
)

// nativeDeliveryBoundary keeps provider echoes out of public step messages.
// The original errors remain available to errors.Is and errors.As.
func nativeDeliveryBoundary(job *Job) func(*error) {
	redactor := nativeDeliveryRedactor(job)
	safeMessage := func(message string) string {
		message, _ = redactor.Line(message, realtime.MaxLineBytes)
		return message
	}
	safeError := func(err error) error {
		if err == nil {
			return nil
		}
		return &nativeDeliveryError{message: safeMessage(err.Error()), cause: err}
	}
	progress, warn := job.Progress, job.warn
	if progress != nil {
		job.Progress = func(status, message string) { progress(status, safeMessage(message)) }
	}
	if warn != nil {
		job.warn = func(err error) { warn(safeError(err)) }
	}
	return func(err *error) {
		job.Progress, job.warn = progress, warn
		*err = safeError(*err)
	}
}

type nativeDeliveryError struct {
	message string
	cause   error
}

func (e *nativeDeliveryError) Error() string { return e.message }
func (e *nativeDeliveryError) Unwrap() error { return e.cause }

func nativeDeliveryRedactor(job *Job) *realtime.Redactor {
	c := job.Collector("", "")
	for key, value := range job.Env {
		if job.Secrets[key] {
			// Credentials embedded in JSON need their individual fields
			// covered when a provider echoes just one invalid field.
			c.addCredential([]byte(value))
		}
	}
	values := append([]string(nil), c.values...)
	for _, value := range c.values {
		if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
			if password, ok := parsed.User.Password(); ok && password != "" {
				values = append(values, password, url.QueryEscape(password), url.PathEscape(password))
			}
		}
	}
	// SDKs may quote a credential, a JSON field or a private key line.
	// Include both JSON and Go escaping before the shared line redactor.
	var escaped []string
	for _, value := range values {
		for _, part := range append([]string{value}, strings.Split(value, "\n")...) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			quoted := strconv.Quote(part)
			escaped = append(escaped, quoted[1:len(quoted)-1])
			encoded, _ := json.Marshal(part) //nolint:errcheck // strings always encode
			escaped = append(escaped, string(encoded[1:len(encoded)-1]))
		}
	}
	return realtime.NewRedactor(append(values, escaped...))
}

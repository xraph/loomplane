package sdk

import "github.com/xraph/loomplane/pkg/provider"

// Protocol is the version of the wire protocol below. Loomplane refuses a
// provider that speaks another.
const Protocol = "loomplane.provider/v1"

// The protocol is JSON over HTTP. Every request carries the token you
// configured, as "Authorization: Bearer <token>".
//
//	GET  /v1/describe            -> Description
//	POST /v1/check               Credential -> Account, or 422 with Failure
//	HEAD /v1/sources/{id}        200 if the provider has that source
//	PUT  /v1/sources/{id}        a gzipped tarball
//	POST /v1/operations          OperationRequest -> 202 Operation
//	GET  /v1/operations/{id}     -> Operation
//	POST /v1/observe             Target -> Observation
//
// Apply and Remove run as operations, because builds and databases take
// minutes: Loomplane starts one, then reads it until it's done, and keeps
// its checkpoints so a retry can resume.

// Description is what GET /v1/describe answers.
type Description struct {
	Protocol string            `json:"protocol"`
	Provider provider.Provider `json:"provider"`
	// Connection tells the dashboard how to ask for a credential.
	Connection ConnectionForm `json:"connection"`
}

// ConnectionForm describes the credential a connection to the provider's
// cloud takes.
type ConnectionForm struct {
	// Title names the cloud, such as "Fly.io".
	Title string `json:"title"`
	// Credential names the secret, such as "Access token".
	Credential string `json:"credential"`
	// Help says where to get it and what it needs.
	Help string `json:"help,omitempty"`
	// Settings are the other fields, such as an organization slug.
	Settings []Setting `json:"settings,omitempty"`
}

// Setting is one field of a connection besides its secret.
type Setting struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required,omitempty"`
}

// Connector is a Provider that describes its connection form. Without it,
// the dashboard asks for a plain credential.
type Connector interface {
	ConnectionForm() ConnectionForm
}

// Failure is an error answer: {"error": "...", "rejected": true} when a
// credential was refused.
type Failure struct {
	Error    string `json:"error"`
	Rejected bool   `json:"rejected,omitempty"`
}

// Kinds of operation.
const (
	OpApply  = "apply"
	OpRemove = "remove"
)

// OperationRequest starts an operation.
type OperationRequest struct {
	Kind string `json:"kind"`
	Job  Job    `json:"job"`
	// Source names a source PUT before, for an Apply that builds.
	Source string `json:"source,omitempty"`
}

// Operation statuses.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Operation is an Apply or Remove in progress or done.
type Operation struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	Progress   string   `json:"progress,omitempty"`
	Checkpoint []byte   `json:"checkpoint,omitempty"`
	Outcome    *Outcome `json:"outcome,omitempty"`
	Error      string   `json:"error,omitempty"`
}

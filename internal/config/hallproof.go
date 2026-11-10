package config

import (
	"errors"
	"fmt"
	"net/url"

	authid "github.com/xraph/authsome/id"
)

// HallproofConfig is where Loomplane pushes compliance evidence. With no
// endpoint it's off, and Loomplane mounts nothing of Hallproof's witness.
type HallproofConfig struct {
	// Endpoint is Hallproof's base URL. Empty turns evidence off.
	Endpoint string
	// IngestKey is this system's ingest key, from Hallproof.
	IngestKey string
	// IngestKeyFile is a file holding the ingest key, such as a mounted
	// secret. Set it or IngestKey, not both.
	IngestKeyFile string
	// StaffOrgs are the Loomplane organizations whose keys, webhooks,
	// agents and jobs are staff evidence, such as the operator's own
	// organization. Empty, nothing an organization owns is collected:
	// every organization is a customer's.
	StaffOrgs []string
}

// Enabled reports whether evidence goes to Hallproof.
func (h HallproofConfig) Enabled() bool { return h.Endpoint != "" }

// String leaves the ingest key out, so the config can be logged.
func (h HallproofConfig) String() string {
	if !h.Enabled() {
		return "hallproof: off"
	}
	return "hallproof: " + h.Endpoint
}

// GoString is String, for %#v.
func (h HallproofConfig) GoString() string { return h.String() }

// Validate refuses an endpoint that isn't https (http only for this
// machine), a key set twice or not at all, and a staff org that isn't an
// organization ID or is named twice.
func (h HallproofConfig) Validate() error {
	if !h.Enabled() {
		return nil
	}
	var errs []error
	u, err := url.Parse(h.Endpoint)
	switch {
	case err != nil || u.Scheme == "" || u.Host == "":
		errs = append(errs, errors.New("hallproof.endpoint must be an absolute URL"))
	case u.User != nil:
		errs = append(errs, errors.New("hallproof.endpoint can't carry user info"))
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		errs = append(errs, errors.New("hallproof.endpoint can't carry a query or fragment"))
	case u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())):
		errs = append(errs, errors.New("hallproof.endpoint must use https (http is allowed only for localhost, 127.0.0.1 and ::1)"))
	}
	switch {
	case h.IngestKey != "" && h.IngestKeyFile != "":
		errs = append(errs, errors.New("set hallproof.ingest_key or hallproof.ingest_key_file, not both"))
	case h.IngestKey == "" && h.IngestKeyFile == "":
		errs = append(errs, errors.New("hallproof.endpoint is set, so it needs hallproof.ingest_key or hallproof.ingest_key_file"))
	}
	seen := make(map[string]bool, len(h.StaffOrgs))
	for _, org := range h.StaffOrgs {
		if _, err := authid.ParseOrgID(org); err != nil {
			errs = append(errs, fmt.Errorf("hallproof.staff_orgs: %q isn't an organization ID (aorg_...)", org))
			continue
		}
		if seen[org] {
			errs = append(errs, fmt.Errorf("hallproof.staff_orgs names %s twice", org))
		}
		seen[org] = true
	}
	return errors.Join(errs...)
}

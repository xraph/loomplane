package connections

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// AccountIdentity fingerprints verified placement scope. Permission results,
// timestamps, principals and credential material do not participate, so you can
// rotate a token or service account within the same deployment account. Missing
// managed account scope, namespace or daemon identities return an empty fingerprint.
func AccountIdentity(cloud, account string, details map[string]string) string {
	scope := map[string]string{"cloud": cloud}
	var keys []string
	switch cloud {
	case GCP:
		if strings.TrimSpace(details["project_id"]) == "" {
			return ""
		}
		keys = []string{"project_id"}
	case Vercel:
		if strings.TrimSpace(details["team_id"]) != "" {
			keys = []string{"team_id"}
		} else {
			if strings.TrimSpace(details["user_id"]) == "" {
				return ""
			}
			keys = []string{"user_id"}
		}
	case Render:
		if strings.TrimSpace(details["owner_id"]) == "" {
			return ""
		}
		keys = []string{"owner_id"}
	case DigitalOcean:
		// A token belongs to one team; an account outside any team is
		// its own scope.
		if strings.TrimSpace(details["team_uuid"]) != "" {
			keys = []string{"team_uuid"}
		} else {
			if strings.TrimSpace(details["account_uuid"]) == "" {
				return ""
			}
			keys = []string{"account_uuid"}
		}
	case Azure:
		// The subscription and resource group are where everything is
		// made; the principal acting there can change.
		if strings.TrimSpace(details["subscription_id"]) == "" || strings.TrimSpace(details["tenant_id"]) == "" {
			return ""
		}
		keys = []string{"tenant_id", "subscription_id", "resource_group"}
	case AWS:
		if strings.TrimSpace(account) == "" || strings.TrimSpace(details["region"]) == "" {
			return ""
		}
		keys = []string{"region"}
	case Kubernetes:
		if strings.TrimSpace(details["namespace_uid"]) == "" {
			return ""
		}
		keys = []string{"endpoint", "namespace", "namespace_uid", "cluster_ca"}
	case Server:
		if strings.TrimSpace(details["docker_id"]) == "" {
			return ""
		}
		keys = []string{"host", "port", "user", "host_key", "docker_socket", "docker_id", "advertise_host"}
	case Registry:
		keys = []string{"endpoint", "repository", "auth_endpoint", "registry_ca"}
	}
	for _, key := range keys {
		if value := strings.TrimSpace(details[key]); value != "" {
			scope[key] = value
		}
	}
	// The cloud's stable scope wins over mutable display names. AWS account
	// numbers are carried in Account itself. Unknown immutable identities
	// return an empty fingerprint and must be verified before use.
	switch cloud {
	case GCP:
		// The verified project ID identifies this account.
	case Vercel:
		// User and team IDs already identify their scopes, including renames.
	case Render:
		// The verified owner ID identifies this workspace.
	case DigitalOcean:
		// The verified team or account UUID identifies this scope.
	case Azure:
		// The verified tenant and subscription IDs identify this scope.
	case Kubernetes:
		if scope["endpoint"] == "" || scope["namespace"] == "" {
			scope["account"] = account
		}
	case Server:
		if scope["host"] == "" || scope["user"] == "" {
			scope["account"] = account
		}
	case Registry:
		if scope["endpoint"] == "" || scope["repository"] == "" {
			scope["account"] = account
		}
	default:
		scope["account"] = account
	}
	// Quote keys and values so boundaries are unambiguous, then sort keys
	// so check-result map insertion order cannot change the fingerprint.
	var raw []byte
	for _, key := range slices.Sorted(maps.Keys(scope)) {
		raw = strconv.AppendQuote(raw, key)
		raw = append(raw, ':')
		raw = strconv.AppendQuote(raw, scope[key])
		raw = append(raw, ',')
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// legacyPersonalVercel needs the stored token checked before a replacement can
// establish its old user identity. Usernames cannot establish that identity.
func legacyPersonalVercel(c *Connection) bool {
	return c.Cloud == Vercel && strings.TrimSpace(c.Details["team_id"]) == "" && strings.TrimSpace(c.Details["user_id"]) == ""
}

func verifiedPersonalVercel(check Check) bool {
	return strings.TrimSpace(check.Details["team_id"]) == "" && strings.TrimSpace(check.Details["user_id"]) != ""
}

// A working owned credential cannot prove a resource's historical identity:
// namespaces and daemons can be recreated while the credential still works.
func recordedOwnedIdentity(c *Connection) error {
	key := ""
	switch c.Cloud {
	case Kubernetes:
		key = "namespace_uid"
	case Server:
		key = "docker_id"
	}
	if key != "" && strings.TrimSpace(c.Details[key]) == "" {
		return fmt.Errorf("%w: this connection has no recorded %s; create a new connection to verify the resource identity", ErrInvalid, key)
	}
	return nil
}

package connections

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// AccountIdentity fingerprints verified placement scope. Permission results,
// timestamps, principals and credential material do not participate, so you can
// rotate a token or service account within the same deployment account.
func AccountIdentity(cloud, account string, details map[string]string) string {
	scope := map[string]string{"cloud": cloud}
	var keys []string
	switch cloud {
	case GCP:
		keys = []string{"project_id"}
	case Vercel:
		keys = []string{"team_id"}
	case Render:
		keys = []string{"owner_id"}
	case AWS:
		keys = []string{"region"}
	case Kubernetes:
		keys = []string{"endpoint", "namespace", "cluster_ca"}
	case Server:
		keys = []string{"host", "port", "user", "host_key", "docker_socket", "advertise_host"}
	case Registry:
		keys = []string{"endpoint", "repository", "auth_endpoint", "registry_ca"}
	}
	for _, key := range keys {
		if value := strings.TrimSpace(details[key]); value != "" {
			scope[key] = value
		}
	}
	// The cloud's stable scope wins over mutable display names. AWS account
	// numbers and personal Vercel usernames are carried in Account itself.
	switch cloud {
	case GCP:
		if scope["project_id"] == "" {
			scope["account"] = account
		}
	case Vercel:
		if scope["team_id"] == "" {
			scope["account"] = account
		}
	case Render:
		if scope["owner_id"] == "" {
			scope["account"] = account
		}
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

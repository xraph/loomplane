package gcp

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLIntentLabel connects a new instance to its checkpointed create request.
const SQLIntentLabel = "loomplane-create-intent"

// Incarnation pins the observed creation time and placement. Cloud SQL does not
// expose an RDS-style resource ID. This is a creation-time pin, not a provider UUID.
func (in *SQLInstance) Incarnation(project, region, name string) (string, error) {
	if project == "" || region == "" || name == "" || in.Project != project || in.Region != region || in.Name != name || in.ConnectionName != project+":"+region+":"+name {
		return "", errors.New("cloud sql incarnation does not match the original project, region and instance")
	}
	created, err := time.Parse(time.RFC3339Nano, in.CreateTime)
	if err != nil || created.IsZero() {
		return "", errors.New("cloud sql incarnation has no valid provider creation time")
	}
	raw, err := json.Marshal([]string{project, region, name, created.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("cloud-sql:%x", sha256.Sum256(raw)), nil
}

// validSQLPathID accepts opaque native IDs without URL/path interpretation.
func validSQLPathID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 256 {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		return !valid
	}) < 0
}

func sqlNativeName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	return strings.IndexFunc(name, func(r rune) bool {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
		return !valid
	}) < 0
}

// Package recipes pins the executable catalog separately from provider targets.
package recipes

import (
	"fmt"
	"strings"
)

// Recipe is an immutable runtime and recovery contract recorded in a plan.
// Adding another image requires a new revision and qualification evidence.
type Recipe struct {
	ID             string `json:"id"`
	Revision       string `json:"revision"`
	ProductVersion string `json:"product_version"`
	Image          string `json:"image"`
	Architecture   string `json:"architecture"`
	MountPath      string `json:"mount_path"`
	BackupFormat   string `json:"backup_format"`
	VolumeSizeMB   int    `json:"volume_size_mb,omitempty"`
}

func DefaultVersion(kind string) string {
	switch kind {
	case "postgres":
		return "17"
	case "mysql":
		return "8.4"
	case "mariadb":
		return "11.8"
	case "redis":
		return "8.2"
	case "valkey":
		return "8.1"
	default:
		return ""
	}
}

// Lookup resolves only qualified engine/version pairs, never a mutable tag.
func Lookup(kind, version, architecture string) (*Recipe, error) {
	if kind == "postgres" {
		return Postgres(version, architecture)
	}
	if kind == "redis" || kind == "valkey" {
		return cacheRecipe(kind, version, architecture)
	}
	var product, format string
	var digests map[string]string
	switch {
	case kind == "mysql" && version == "8.4":
		product, format = "8.4.11", "mysqldump-sql-v1"
		digests = map[string]string{"amd64": "sha256:1f8411cc085d2f2f92060fca926746cbf97f87b76c3008e9741ad62e604c4ffa", "arm64": "sha256:88b9c60a44c8864787b96e8254cd050c4af5e82fea6edf08225546f447b43a31"}
	case kind == "mariadb" && version == "11.8":
		product, format = "11.8.9", "mariadb-dump-sql-v1"
		digests = map[string]string{"amd64": "sha256:d689f65e09e887e96db5c40ca3957dd9f4e3fad3516baec96d7726c78a5fc2ee", "arm64": "sha256:3607e75203fd9f69fb7e0829ec2d8fe55c30ec9d8aa266f7580719711bb4a4e1"}
	default:
		return nil, fmt.Errorf("%s %s has no supported recipe", kind, version)
	}
	digest, ok := digests[architecture]
	if !ok {
		return nil, fmt.Errorf("%s recipe requires a checked amd64 or arm64 target, got %q", kind, architecture)
	}
	return &Recipe{ID: kind + "-" + version, Revision: "1", ProductVersion: product, Image: "docker.io/library/" + kind + "@" + digest, Architecture: architecture, MountPath: "/var/lib/mysql", BackupFormat: format}, nil
}

func (r *Recipe) Engine() string {
	if r == nil {
		return ""
	}
	switch {
	case strings.HasPrefix(r.ID, "postgresql-"):
		return "postgres"
	case strings.HasPrefix(r.ID, "mariadb-"):
		return "mariadb"
	case strings.HasPrefix(r.ID, "mysql-"):
		return "mysql"
	case strings.HasPrefix(r.ID, "redis-"):
		return "redis"
	case strings.HasPrefix(r.ID, "valkey-"):
		return "valkey"
	default:
		return ""
	}
}
func (r *Recipe) Port() int {
	switch r.Engine() {
	case "postgres":
		return 5432
	case "mysql", "mariadb":
		return 3306
	case "redis", "valkey":
		return 6379
	default:
		return 0
	}
}

// RuntimeArgs belongs to recipe revision 1's single-instance logical profile.
// Binary logs are unused here; copies must never execute restored timers.
func (r *Recipe) RuntimeArgs(recovery bool) []string {
	if r.Cache() {
		return r.cacheArgs(recovery)
	}
	if r.Engine() != "mysql" && r.Engine() != "mariadb" {
		return nil
	}
	args := []string{"--skip-log-bin"}
	if recovery {
		scheduler := "DISABLED"
		if r.Engine() == "mariadb" {
			// MariaDB refuses event DDL with DISABLED. OFF permits import;
			// enabling it still requires global privileges the app lacks.
			scheduler = "OFF"
		}
		args = append(args, "--event-scheduler="+scheduler)
	}
	return args
}

// Postgres pins the official PostgreSQL 17.11 Alpine image. These manifests
// belong to index sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24.
// Source: docker-library/postgres revision 2603e26e245e558218728ee14e0a42dcb020dc7f.
func Postgres(version, architecture string) (*Recipe, error) {
	if version != "17" {
		return nil, fmt.Errorf("PostgreSQL %s has no supported recipe", version)
	}
	digests := map[string]string{
		"amd64": "sha256:aa90e97ee862e558111d34cfb8b2c4bec768c2b039fb791341686928560263b3",
		"arm64": "sha256:0b2882c46a2b8d8e148431b333394348cf4d7f15aec6df7f17d51dc641e56cfa",
	}
	digest, ok := digests[architecture]
	if !ok {
		return nil, fmt.Errorf("PostgreSQL recipe requires a checked amd64 or arm64 target, got %q", architecture)
	}
	return &Recipe{ID: "postgresql-17", Revision: "1", ProductVersion: "17.11", Architecture: architecture, Image: "docker.io/library/postgres@" + digest, MountPath: "/var/lib/postgresql/data", BackupFormat: "pg-dump-custom-v1"}, nil
}

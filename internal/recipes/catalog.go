// Package recipes pins the executable catalog separately from provider targets.
package recipes

import "fmt"

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

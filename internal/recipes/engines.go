package recipes

import (
	"fmt"
	"strings"
)

// families are the native adapter families and each one's full-backup
// format. An Engine reuses one of them.
var families = map[string]string{
	"postgres": "pg-dump-custom-v1",
	"mysql":    "mysqldump-sql-v1",
	"mariadb":  "mariadb-dump-sql-v1",
	"redis":    "redis-rdb-v1",
	"valkey":   "valkey-rdb-v1",
}

// builtinEngines describes the five engines the catalog has always had.
// Their recipes still come from Lookup and KubernetesPostgres: each release
// here is read back from those tables, so every pin stays in one place.
func builtinEngines() []Engine {
	return []Engine{
		builtinEngine("postgres", "17", "PostgreSQL", "DATABASE_URL", "server", "kubernetes"),
		builtinEngine("mysql", "8.4", "MySQL", "DATABASE_URL", "server"),
		builtinEngine("mariadb", "11.8", "MariaDB", "DATABASE_URL", "server"),
		builtinEngine("redis", "8.2", "Redis", "REDIS_URL", "server"),
		builtinEngine("valkey", "8.1", "Valkey", "REDIS_URL", "server"),
	}
}

func builtinEngine(kind, version, title, envKey string, targets ...string) Engine {
	release := EngineRelease{Version: version, Image: Image{Digests: map[string]string{}}}
	for _, architecture := range []string{"amd64", "arm64"} {
		recipe, err := Lookup(kind, version, architecture)
		if err != nil {
			panic(fmt.Sprintf("built-in %s recipe: %v", kind, err))
		}
		repository, digest, _ := strings.Cut(recipe.Image, "@")
		release.Image.Repository, release.Image.Digests[architecture] = repository, digest
		release.Product, release.Revision = recipe.ProductVersion, recipe.Revision
		release.MountPath, release.BackupFormat = recipe.MountPath, recipe.BackupFormat
	}
	if kind == "postgres" {
		cluster, err := KubernetesPostgres(version, "amd64")
		if err != nil {
			panic(fmt.Sprintf("built-in %s recipe: %v", kind, err))
		}
		release.KubernetesVolumeMB = cluster.VolumeSizeMB
	}
	return Engine{
		Kind: kind, Family: kind, Title: title, EnvKey: envKey,
		Placement: PlacementServiceProject, Targets: targets,
		Releases: []EngineRelease{release},
		builtin:  true,
	}
}

// builtinLookup is LookupForTarget as it was before the registry, for the
// five built-in engines.
func builtinLookup(kind, version, architecture, target string) (*Recipe, error) {
	switch target {
	case "server":
		return Lookup(kind, version, architecture)
	case "kubernetes":
		if kind == "postgres" {
			return KubernetesPostgres(version, architecture)
		}
	}
	return nil, fmt.Errorf("%s has no qualified %s database recipe", target, kind)
}

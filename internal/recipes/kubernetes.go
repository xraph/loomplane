package recipes

import "fmt"

// KubernetesPostgres pins the standalone cluster profile separately from Docker.
// The request is a PVC capacity, not a claim of replicated or reserved host disk.
func KubernetesPostgres(version, architecture string) (*Recipe, error) {
	recipe, err := Postgres(version, architecture)
	if err != nil {
		return nil, err
	}
	recipe.ID += "-kubernetes"
	recipe.VolumeSizeMB = 10240
	return recipe, nil
}

// KubernetesCache pins the Redis or Valkey profile a cluster runs as a
// composition's dependency. Kubernetes doesn't run a standalone cache: the
// planner admits this recipe only for a dependency.
func KubernetesCache(kind, version, architecture string) (*Recipe, error) {
	if kind != "redis" && kind != "valkey" {
		return nil, fmt.Errorf("%s is not a cache", kind)
	}
	recipe, err := cacheRecipe(kind, version, architecture)
	if err != nil {
		return nil, err
	}
	recipe.ID += "-kubernetes"
	recipe.VolumeSizeMB = 2048
	return recipe, nil
}

// KubernetesRecovery reports whether Loomplane backs up and restores a
// kind on Kubernetes. Only PostgreSQL has a qualified cluster adapter.
func KubernetesRecovery(kind string) bool { return kind == "postgres" }

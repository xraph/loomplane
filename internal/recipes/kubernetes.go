package recipes

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

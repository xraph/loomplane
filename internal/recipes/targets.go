package recipes

import "fmt"

// LookupForTarget keeps platform-specific storage contracts in the reviewed recipe.
func LookupForTarget(kind, version, architecture, target string) (*Recipe, error) {
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

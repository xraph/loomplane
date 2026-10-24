package deployments

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func databaseVolume(recipe *recipes.Recipe) cpprovider.VolumeSpec {
	return cpprovider.VolumeSpec{Name: "data", Type: "persistent", MountPath: recipe.MountPath, SizeMB: recipe.VolumeSizeMB}
}
func databaseAddress(cfg *planning.ResourceConfig, iid id.ID, name, password string) (string, error) {
	if cfg == nil || cfg.Catalog == nil || cfg.Catalog.Port() == 0 {
		return "", errors.New("database address requires a reviewed recipe")
	}
	host := server.ContainerName(iid)
	switch cfg.Provider {
	case "server":
	case "kubernetes":
		if err := planning.ValidateKubernetesDatabaseSettings(cfg.TargetSettings); err != nil {
			return "", err
		}
		host = kubernetes.WorkloadName(iid) + "." + cfg.TargetSettings["namespace"] + ".svc"
	default:
		return "", errors.New("database address has no qualified target")
	}
	scheme, suffix := "mysql", ""
	if cfg.Catalog.Engine() == "rabbitmq" {
		return fmt.Sprintf("amqp://%s@%s:5672/%%2F", url.UserPassword(sqlUser, password).String(), host), nil
	}
	if cfg.Catalog.Engine() == "nats" {
		return fmt.Sprintf("nats://%s@%s:4222", url.UserPassword(sqlUser, password).String(), host), nil
	}
	if cfg.Catalog.Engine() == "postgres" {
		scheme, suffix = "postgresql", "?sslmode=disable"
	}
	if cfg.Catalog.Cache() {
		scheme, name = "redis", "0"
	}
	return fmt.Sprintf("%s://%s@%s:%d/%s%s", scheme, url.UserPassword(sqlUser, password).String(), host, cfg.Catalog.Port(), name, suffix), nil
}

func (e *OwnedTarget) checkDatabaseBinding(binding *planning.Binding, cfg *planning.ResourceConfig) error {
	if binding == nil || cfg == nil || !planning.OwnedCloud(e.Cloud) || binding.Subject != planning.ResourceSubject || binding.Provider != e.Cloud || cfg.Provider != e.Cloud || cfg.Target == "" || cfg.Target != binding.ConnectionID {
		return errors.New("database binding does not match this target adapter")
	}
	return nil
}

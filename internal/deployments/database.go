package deployments

import (
	"errors"
	"reflect"

	nativemysql "github.com/xraph/loomplane/internal/clouds/mysql"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

func databaseService(cfg *planning.ResourceConfig) (*planning.ServiceConfig, error) {
	if cfg == nil || !planning.OwnedCloud(cfg.Provider) || cfg.Target == "" || cfg.Catalog == nil {
		return nil, errors.New("database needs a reviewed connected-target recipe")
	}
	expected, err := recipes.LookupForTarget(cfg.Kind, cfg.Version, cfg.TargetSettings["architecture"], cfg.Provider)
	if err != nil {
		return nil, err
	}
	if cfg.Provider == "kubernetes" {
		if err := planning.ValidateKubernetesDatabaseSettings(cfg.TargetSettings); err != nil {
			return nil, err
		}
	}
	if !reflect.DeepEqual(cfg.Catalog, expected) {
		return nil, errors.New("the database recipe changed; create a new plan")
	}
	return &planning.ServiceConfig{Provider: cfg.Provider, Target: cfg.Target, TargetSettings: cfg.TargetSettings, Workload: provider.Worker, Execution: provider.Container, Port: expected.Port(), Catalog: cfg.Catalog, BuildTarget: &planning.BuildTarget{Image: cfg.Catalog.Image}}, nil
}

func databaseEnvironment(kind, name, application, admin string) (env map[string]string, secrets map[string]bool) {
	if nativecache.ValidEngine(kind) {
		return map[string]string{"LOOMPLANE_CACHE_ADMIN_PASSWORD": admin, "LOOMPLANE_APP_PASSWORD": application}, map[string]bool{"LOOMPLANE_CACHE_ADMIN_PASSWORD": true, "LOOMPLANE_APP_PASSWORD": true}
	}
	if kind == "postgres" {
		return map[string]string{"POSTGRES_DB": name, "POSTGRES_PASSWORD": admin}, map[string]bool{"POSTGRES_PASSWORD": true}
	}
	prefix := "MYSQL_"
	if kind == "mariadb" {
		prefix = "MARIADB_"
	}
	return map[string]string{prefix + "DATABASE": name, prefix + "ROOT_PASSWORD": admin, "LOOMPLANE_APP_PASSWORD": application}, map[string]bool{prefix + "ROOT_PASSWORD": true, "LOOMPLANE_APP_PASSWORD": true}
}

func databaseLabels(labels map[string]string, recipe *recipes.Recipe, name string) {
	labels[owned.LabelStateful] = "true"
	labels[labelImage] = recipe.Image
	if recipe.Cache() {
		labels[nativecache.LabelEngine] = recipe.Engine()
		return
	}
	if recipe.Engine() == "postgres" {
		labels[server.LabelDatabase] = name
		return
	}
	labels[nativemysql.LabelDatabase] = name
	labels[nativemysql.LabelEngine] = recipe.Engine()
}

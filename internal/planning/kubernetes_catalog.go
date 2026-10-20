package planning

import (
	"errors"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ValidateKubernetesDatabaseSettings validates the persistent destination pinned
// in a plan. Live recovery permission and network enforcement checks are separate.
func ValidateKubernetesDatabaseSettings(settings map[string]string) error {
	if settings["architecture"] != "amd64" && settings["architecture"] != "arm64" {
		return errors.New("choose an explicit amd64 or arm64 database architecture")
	}
	if settings["namespace"] == "" || len(validation.IsDNS1123Label(settings["namespace"])) > 0 || settings["namespace_uid"] == "" {
		return errors.New("check the database namespace and its identity")
	}
	if settings["storage_class"] == "" || len(validation.IsDNS1123Subdomain(settings["storage_class"])) > 0 || settings["storage_class_uid"] == "" || settings["storage_reclaim"] != "Retain" || settings["persistent_storage"] != "ready" {
		return errors.New("select and check a retained filesystem storage class for the database")
	}
	return nil
}

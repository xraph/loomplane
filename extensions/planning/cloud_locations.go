package planning

import (
	"context"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// CloudLocationList contains only the locations visible in the requested scope.
type CloudLocationList struct {
	Locations []cloudprofiles.CloudLocation `json:"locations"`
}

func (CloudLocationList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "locations"}
}

func (e *Extension) cloudLocationRoutes() []cloudRoute {
	return []cloudRoute{
		{http.MethodDelete, "/clouds/:cloudId/locations/:locationId", "cloudLocations.delete", e.deleteCloudLocation, CloudLocationDeleteInput{}, nil, http.StatusNoContent},
		{http.MethodPost, "/clouds/:cloudId/locations/:locationId/verification", "cloudLocations.verify", e.verifyCloudLocation, cloudprofiles.LocationVerificationInput{}, cloudprofiles.CloudLocation{}, http.StatusOK},
		{http.MethodGet, "/clouds/:cloudId/locations", "cloudLocations.list", e.listCloudLocations, nil, CloudLocationList{}, http.StatusOK},
		{http.MethodPost, "/clouds/:cloudId/locations", "cloudLocations.save", e.saveCloudLocation, cloudprofiles.LocationInput{}, cloudprofiles.CloudLocation{}, http.StatusOK},
		{http.MethodGet, "/clouds/:cloudId/locations/:locationId", "cloudLocations.get", e.getCloudLocation, nil, cloudprofiles.CloudLocation{}, http.StatusOK},
		{http.MethodGet, "/projects/:id/clouds/:cloudId/locations", "projectCloudLocations.list", e.listCloudLocations, nil, CloudLocationList{}, http.StatusOK},
		{http.MethodGet, "/projects/:id/clouds/:cloudId/locations/:locationId", "projectCloudLocations.get", e.getCloudLocation, nil, cloudprofiles.CloudLocation{}, http.StatusOK},
	}
}
func (e *Extension) listCloudLocations(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		var locations []cloudprofiles.CloudLocation
		var err error
		if c.Param("id") != "" {
			locations, err = e.cloudProfiles.ListProjectLocations(ctx, p, c.Param("id"), c.Param("cloudId"))
		} else {
			locations, err = e.cloudProfiles.ListLocations(ctx, p, c.Param("cloudId"))
		}
		return CloudLocationList{Locations: locations}, err
	})
}
func (e *Extension) getCloudLocation(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		if c.Param("id") != "" {
			return e.cloudProfiles.GetProjectLocation(ctx, p, c.Param("id"), c.Param("cloudId"), c.Param("locationId"))
		}
		return e.cloudProfiles.GetLocation(ctx, p, c.Param("cloudId"), c.Param("locationId"))
	})
}
func (e *Extension) saveCloudLocation(c forge.Context) error {
	in, err := cloudBody[cloudprofiles.LocationInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.SaveLocation(ctx, p, c.Param("cloudId"), in)
	})
}

func (e *Extension) verifyCloudLocation(c forge.Context) error {
	in, err := cloudBody[cloudprofiles.LocationVerificationInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.VerifyLocation(ctx, p, c.Param("cloudId"), c.Param("locationId"), in)
	})
}

// CloudLocationDeleteInput pins the reviewed Cloud and location before deletion.
type CloudLocationDeleteInput struct {
	CloudRevision int64 `json:"cloud_revision"`
	Revision      int64 `json:"revision"`
}

func (e *Extension) deleteCloudLocation(c forge.Context) error {
	in, err := cloudBody[CloudLocationDeleteInput](c)
	if err != nil {
		return err
	}
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := e.cloudProfiles.DeleteLocation(c.Request().Context(), p, c.Param("cloudId"), c.Param("locationId"), in.Revision, in.CloudRevision); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

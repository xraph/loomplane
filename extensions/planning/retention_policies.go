package planning

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/recovery"
)

func (e *Extension) retentionPolicyRoutes(group forge.Router) error {
	if err := group.GET("/service-backups/:id/retention-policy", e.getRetentionPolicy, forge.WithName("retentionPolicies.get"), forge.WithResponseSchema(http.StatusOK, "Source-bound automatic retention policy", recovery.RetentionPolicyState{})); err != nil {
		return err
	}
	if err := group.PUT("/service-backups/:id/retention-policy", e.setRetentionPolicy, forge.WithName("retentionPolicies.set"), forge.WithInvalidates("RetentionPolicy[]"), forge.WithRequestSchema(recovery.RetentionPolicyInput{}), forge.WithResponseSchema(http.StatusOK, "Saved retention policy revision", recovery.RetentionPolicy{})); err != nil {
		return err
	}
	return group.GET("/service-backups/:id/retention-policy/history", e.retentionPolicyHistory, forge.WithName("retentionPolicies.history"), forge.WithQuerySchema(recovery.RetentionPolicyHistoryInput{}), forge.WithResponseSchema(http.StatusOK, "Committed retention policy revisions", recovery.RetentionPolicyHistory{}))
}
func (e *Extension) getRetentionPolicy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	policy, err := e.backups.RetentionPolicyForBackup(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, recovery.RetentionPolicyState{Configured: policy != nil, Policy: policy})
}
func (e *Extension) setRetentionPolicy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var input recovery.RetentionPolicyInput
	if err = c.BindJSON(&input); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide revision, policy settings and enabled")
	}
	policy, err := e.backups.SetRetentionPolicy(c.Request().Context(), p, c.Param("id"), input)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, policy)
}
func (e *Extension) retentionPolicyHistory(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	before, limit, err := retirementPageQuery(c)
	if err != nil {
		return err
	}
	page, err := e.backups.RetentionPolicyHistoryForBackup(c.Request().Context(), p, c.Param("id"), recovery.RetentionPolicyHistoryInput{Before: before, Limit: limit})
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, page)
}

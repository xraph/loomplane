package planning

import (
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/recovery"
)

func (e *Extension) nativeRetentionPolicyRoutes(group forge.Router) error {
	if err := group.GET("/native-recovery-sources/:id/retention-policy", e.getNativeRetentionPolicy, forge.WithName("nativeRetentionPolicies.get"), forge.WithResponseSchema(http.StatusOK, "Configured native retention policy", recovery.NativeRetentionPolicyState{})); err != nil {
		return err
	}
	if err := group.PUT("/native-recovery-sources/:id/retention-policy", e.setNativeRetentionPolicy, forge.WithName("nativeRetentionPolicies.set"), forge.WithRequestSchema(recovery.RetentionPolicyInput{}), forge.WithResponseSchema(http.StatusOK, "Saved native retention revision", recovery.NativeRetentionPolicy{})); err != nil {
		return err
	}
	return group.GET("/native-recovery-sources/:id/retention-policy/history", e.nativeRetentionPolicyHistory, forge.WithName("nativeRetentionPolicies.history"), forge.WithQuerySchema(recovery.RetentionPolicyHistoryInput{}), forge.WithResponseSchema(http.StatusOK, "Committed native policy revisions", recovery.RetentionPolicyHistory{}))
}
func (e *Extension) getNativeRetentionPolicy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	policy, err := e.nativeBackups.NativeRetentionPolicy(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, recovery.NativeRetentionPolicyState{Configured: policy != nil, Policy: policy})
}
func (e *Extension) setNativeRetentionPolicy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in recovery.RetentionPolicyInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide the observed revision, retention settings and enabled state")
	}
	policy, err := e.nativeBackups.SetNativeRetentionPolicy(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return nativeCopyError(err)
	}
	return c.JSON(http.StatusOK, policy)
}
func (e *Extension) nativeRetentionPolicyHistory(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	in := recovery.RetentionPolicyHistoryInput{Before: c.Query("before")}
	if raw := c.Query("limit"); raw != "" {
		limit, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "provide limit from 1 to 100")
		}
		in.Limit = limit
	}
	page, err := e.nativeBackups.NativeRetentionPolicyHistory(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, page)
}

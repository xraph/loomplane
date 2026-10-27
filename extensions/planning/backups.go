package planning

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

const retirementJob = "loomplane.service.backup.retirement"

const restoreJob = "loomplane.service.restore"

const backupJob = "loomplane.service.backup"
const reconcileBackupsJob = "loomplane.service.backup.reconcile"
const nativeRestoreJob = "loomplane.service.native.restore"
const nativeCleanupJob = "loomplane.service.native.cleanup"
const nativeRetirementJob = "loomplane.service.native.retirement"

type nativeRecoveryBackend interface {
	recovery.NativeDeletionBackend
	recovery.NativeBackend
	recovery.NativeCopyBackend
	recovery.NativeCleanupBackend
	recovery.NativeAccessBackend
}

type backupPayload struct {
	OrgID string `json:"org_id"`
	ID    string `json:"id"`
}
type BackupList struct {
	Backups []recovery.Backup `json:"backups"`
}

func (BackupList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "backups"} }

func (e *Extension) setupBackups(app forge.App, pg *pgdriver.PgDB, prj *projects.PgStore, plans *planning.PgStore, authz *security.Authorizer, target recovery.Targets, native nativeRecoveryBackend, audit projects.Auditor) error {
	files, err := forge.InjectType[*backupfiles.Store](app.Container())
	if err != nil {
		return err
	}
	records := recovery.NewPgStore(pg)
	reviewKey, err := security.DeriveKey(e.cfg.SecretKey, recovery.NativeReviewKeyLabel)
	if err != nil {
		return err
	}
	e.nativeBackups = &recovery.NativeService{
		Retirements: records, DeletionBackend: native, EnqueueDeletion: e.enqueueRecovery(nativeRetirementJob, 0),
		Store: records, Authz: authz, Projects: prj, Backend: native, Admission: records,
		ReviewKey: reviewKey, Audit: audit,
		Copies: records, CopyBackend: native, AccessBackend: native, EnqueueCopy: e.enqueueRecovery(nativeRestoreJob, 0),
		Cleanups: records, CleanupBackend: native, EnqueueCleanup: e.enqueueRecovery(nativeCleanupJob, 0),
	}
	e.backups = &recovery.Service{
		RetentionPolicies: records, Policies: records, Restores: records, Retirements: records, Store: records,
		RestoreBackend: target, Backend: target, Projects: prj, Bindings: plans, Authz: authz, Audit: audit, Files: files,
		Enqueue: e.enqueueRecovery(backupJob, 2), EnqueueRestore: e.enqueueRecovery(restoreJob, 0), EnqueueRetirement: e.enqueueRecovery(retirementJob, 0),
	}

	engine.Register(e.engineOf(), job.NewDefinition(retirementJob, func(ctx context.Context, in backupPayload) error {
		return e.backups.RunRetirement(ctx, in.OrgID, in.ID)
	}))
	engine.Register(e.engineOf(), job.NewDefinition(restoreJob, func(ctx context.Context, in backupPayload) error { return e.backups.RunRestore(ctx, in.OrgID, in.ID) }))
	engine.Register(e.engineOf(), job.NewDefinition(backupJob, func(ctx context.Context, in backupPayload) error { return e.backups.Run(ctx, in.OrgID, in.ID) }))
	engine.Register(e.engineOf(), job.NewDefinition(nativeRetirementJob, func(ctx context.Context, in backupPayload) error {
		return e.nativeBackups.RunNativeRetirement(ctx, in.OrgID, in.ID)
	}))
	engine.Register(e.engineOf(), job.NewDefinition(nativeRestoreJob, func(ctx context.Context, in backupPayload) error {
		return e.nativeBackups.RunCopy(ctx, in.OrgID, in.ID)
	}))
	engine.Register(e.engineOf(), job.NewDefinition(nativeCleanupJob, func(ctx context.Context, in backupPayload) error {
		return e.nativeBackups.RunCleanup(ctx, in.OrgID, in.ID)
	}))
	engine.Register(e.engineOf(), job.NewDefinition(reconcileBackupsJob, func(ctx context.Context, _ orchestration.DriftPayload) error { return e.reconcileRecovery(ctx) }))
	return nil
}

func (e *Extension) reconcileRecovery(ctx context.Context) error {
	return errors.Join(e.backups.Reconcile(ctx), e.nativeBackups.Reconcile(ctx))
}

func (e *Extension) enqueueRecovery(name string, retries int) func(context.Context, string, string) error {
	return func(ctx context.Context, org, id string) error {
		_, err := engine.Enqueue(ctx, e.engineOf(), name, backupPayload{OrgID: org, ID: id}, job.WithQueue(orchestration.Queue), job.WithMaxRetries(retries), job.WithTimeout(45*time.Minute))
		return err
	}
}

func (e *Extension) scheduleBackupReconciliation(ctx context.Context) error {
	return engine.RegisterCron(ctx, e.engineOf(), &cron.Definition[orchestration.DriftPayload]{Name: reconcileBackupsJob, JobName: reconcileBackupsJob, Schedule: "@every 1m", Queue: orchestration.Queue})
}

func (e *Extension) backupRoutes(router forge.Router, guard identity.Guard) error {
	group := router.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Service backups"))
	if err := e.nativeBackupRoutes(group); err != nil {
		return err
	}
	if err := group.GET("/projects/:id/service-backups", e.listBackups, forge.WithName("serviceBackups.list"), forge.WithResponseSchema(http.StatusOK, "Database recovery points", BackupList{})); err != nil {
		return err
	}
	if err := group.POST("/projects/:id/service-backups", e.createBackup, forge.WithName("serviceBackups.create"), forge.WithRequestSchema(recovery.BackupCreateInput{}), forge.WithResponseSchema(http.StatusAccepted, "Queued database backup", recovery.Backup{})); err != nil {
		return err
	}
	if err := group.GET("/service-backups/:id", e.getBackup, forge.WithName("serviceBackups.get"), forge.WithResponseSchema(http.StatusOK, "Database recovery point", recovery.Backup{})); err != nil {
		return err
	}
	if err := group.GET("/service-backups/:id/retention-preview", e.previewBackupRetention, forge.WithName("serviceBackups.previewRetention"), forge.WithQuerySchema(recovery.RetentionInput{}), forge.WithResponseSchema(http.StatusOK, "Read-only full-backup retention preview", recovery.RetentionPreview{})); err != nil {
		return err
	}
	if err := group.POST("/service-backups/:id/retry", e.retryBackup, forge.WithName("serviceBackups.retry"), forge.WithResponseSchema(http.StatusAccepted, "Retried database backup", recovery.Backup{})); err != nil {
		return err
	}
	if err := e.retentionPolicyRoutes(group); err != nil {
		return err
	}
	if err := e.restoreRoutes(group); err != nil {
		return err
	}
	if err := e.retirementRoutes(group); err != nil {
		return err
	}
	return e.policyRoutes(group)
}

func (e *Extension) previewBackupRetention(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	days, err := strconv.Atoi(c.Query("older_than_days"))
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide older_than_days as an integer from 1 to 3650")
	}
	keep, err := strconv.Atoi(c.Query("keep_last"))
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide keep_last as an integer from 1 to 1000")
	}
	preview, err := e.backups.PreviewRetention(c.Request().Context(), p, c.Param("id"), recovery.RetentionInput{OlderThanDays: days, KeepLast: keep})
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, preview)
}
func (e *Extension) listBackups(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.backups.List(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, BackupList{Backups: list})
}
func (e *Extension) createBackup(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var input recovery.BackupCreateInput
	if err = c.BindJSON(&input); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide environment, resource and request_id")
	}
	record, err := e.backups.Create(c.Request().Context(), p, c.Param("id"), input)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, record)
}
func (e *Extension) getBackup(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	record, err := e.backups.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, record)
}
func (e *Extension) retryBackup(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	record, err := e.backups.Retry(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, record)
}

type RestoreList struct {
	Restores []recovery.Restore `json:"restores"`
}

func (RestoreList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "restores"}
}
func (e *Extension) restoreRoutes(group forge.Router) error {
	if err := group.POST("/service-backups/:id/restore", e.createRestore, forge.WithName("serviceRestores.create"), forge.WithRequestSchema(recovery.RestoreInput{}), forge.WithResponseSchema(http.StatusAccepted, "Queued isolated recovery copy", recovery.Restore{})); err != nil {
		return err
	}
	if err := group.GET("/projects/:id/service-restores", e.listRestores, forge.WithName("serviceRestores.list"), forge.WithResponseSchema(http.StatusOK, "Isolated recovery copies", RestoreList{})); err != nil {
		return err
	}
	if err := group.GET("/service-restores/:id", e.getRestore, forge.WithName("serviceRestores.get"), forge.WithResponseSchema(http.StatusOK, "Isolated recovery copy", recovery.Restore{})); err != nil {
		return err
	}
	return group.DELETE("/service-restores/:id", e.deleteRestore, forge.WithName("serviceRestores.delete"))
}
func (e *Extension) createRestore(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var input recovery.RestoreInput
	if err = c.BindJSON(&input); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide request_id")
	}
	r, err := e.backups.Restore(c.Request().Context(), p, c.Param("id"), input)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, r)
}
func (e *Extension) getRestore(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := e.backups.GetRestore(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}
func (e *Extension) listRestores(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.backups.ListRestores(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, RestoreList{Restores: list})
}
func (e *Extension) deleteRestore(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.backups.DeleteRestore(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

type PolicyList struct {
	Policies []recovery.Policy `json:"policies"`
}

func (PolicyList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "policies"} }
func (e *Extension) policyRoutes(group forge.Router) error {
	if err := group.GET("/projects/:id/backup-policies", e.listBackupPolicies, forge.WithName("backupPolicies.list"), forge.WithResponseSchema(http.StatusOK, "Scheduled backup policies", PolicyList{})); err != nil {
		return err
	}
	return group.PUT("/projects/:id/backup-policies", e.setBackupPolicy, forge.WithName("backupPolicies.set"), forge.WithRequestSchema(recovery.PolicyInput{}), forge.WithResponseSchema(http.StatusOK, "Scheduled backup policy", recovery.Policy{}))
}
func (e *Extension) listBackupPolicies(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.backups.ListPolicies(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, PolicyList{Policies: list})
}
func (e *Extension) setBackupPolicy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var input recovery.PolicyInput
	if err = c.BindJSON(&input); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide environment, resource, every_hours and enabled")
	}
	r, err := e.backups.SetPolicy(c.Request().Context(), p, c.Param("id"), input)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}

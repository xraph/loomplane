import type { Codec } from '../codec-runtime';

export const codec_Rollback: Codec = {"kind": "object", "fields": {"reason": {"ts": "reason"}, "restore_deployment_id": {"ts": "restoreDeploymentId"}, "restore_finished_at": {"ts": "restoreFinishedAt"}, "restore_plan_id": {"ts": "restorePlanId"}, "target_deployment_id": {"ts": "targetDeploymentId"}, "target_plan_id": {"ts": "targetPlanId"}, "trigger": {"ts": "trigger"}, "watch_id": {"ts": "watchId"}}, "required": ["reason", "restore_deployment_id", "restore_finished_at", "restore_plan_id", "target_deployment_id", "target_plan_id", "trigger"]};

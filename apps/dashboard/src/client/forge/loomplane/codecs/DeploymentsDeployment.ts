import type { Codec } from '../codec-runtime';
import { codec_Canary } from './Canary';
import { codec_CapacityWait } from './CapacityWait';
import { codec_DeploymentsDeployment_sources } from './DeploymentsDeployment.sources';
import { codec_DeploymentsDeployment_steps } from './DeploymentsDeployment.steps';

export const codec_DeploymentsDeployment: Codec = {"kind": "object", "fields": {"canary": {"ts": "canary", "codec": () => codec_Canary}, "capacity_wait": {"ts": "capacityWait", "codec": () => codec_CapacityWait}, "commit_sha": {"ts": "commitSha"}, "environment": {"ts": "environment"}, "environment_id": {"ts": "environmentId"}, "error": {"ts": "error"}, "finished_at": {"ts": "finishedAt"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "plan_id": {"ts": "planId"}, "project_id": {"ts": "projectId"}, "retries": {"ts": "retries"}, "sources": {"ts": "sources", "codec": () => codec_DeploymentsDeployment_sources}, "started_at": {"ts": "startedAt"}, "started_by": {"ts": "startedBy"}, "status": {"ts": "status"}, "steps": {"ts": "steps", "codec": () => codec_DeploymentsDeployment_steps}}, "required": ["commit_sha", "environment", "environment_id", "error", "id", "org_id", "plan_id", "project_id", "retries", "sources", "started_at", "started_by", "status", "steps"]};

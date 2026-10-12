import type { Codec } from '../codec-runtime';
import { codec_Deployment_sources } from './Deployment.sources';
import { codec_Deployment_steps } from './Deployment.steps';

export const codec_Deployment: Codec = {"kind": "object", "fields": {"commit_sha": {"ts": "commitSha"}, "environment": {"ts": "environment"}, "environment_id": {"ts": "environmentId"}, "error": {"ts": "error"}, "finished_at": {"ts": "finishedAt"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "plan_id": {"ts": "planId"}, "project_id": {"ts": "projectId"}, "sources": {"ts": "sources", "codec": () => codec_Deployment_sources}, "started_at": {"ts": "startedAt"}, "started_by": {"ts": "startedBy"}, "status": {"ts": "status"}, "steps": {"ts": "steps", "codec": () => codec_Deployment_steps}}, "required": ["commit_sha", "environment", "environment_id", "error", "id", "org_id", "plan_id", "project_id", "sources", "started_at", "started_by", "status", "steps"]};

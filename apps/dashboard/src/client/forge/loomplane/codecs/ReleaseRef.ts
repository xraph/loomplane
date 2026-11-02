import type { Codec } from '../codec-runtime';

export const codec_ReleaseRef: Codec = {"kind": "object", "fields": {"deployment_id": {"ts": "deploymentId"}, "finished_at": {"ts": "finishedAt"}, "kind": {"ts": "kind"}, "plan_id": {"ts": "planId"}, "status": {"ts": "status"}}, "required": ["deployment_id", "finished_at", "kind", "plan_id", "status"]};

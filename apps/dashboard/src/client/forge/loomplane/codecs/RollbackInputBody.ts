import type { Codec } from '../codec-runtime';

export const codec_RollbackInputBody: Codec = {"kind": "object", "fields": {"deployment_id": {"ts": "deploymentId"}, "environment": {"ts": "environment"}, "reason": {"ts": "reason"}}, "required": ["environment", "reason"]};

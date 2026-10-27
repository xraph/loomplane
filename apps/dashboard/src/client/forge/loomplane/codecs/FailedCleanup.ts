import type { Codec } from '../codec-runtime';

export const codec_FailedCleanup: Codec = {"kind": "object", "fields": {"attempt": {"ts": "attempt"}, "deployment_id": {"ts": "deploymentId"}, "error": {"ts": "error"}, "revision": {"ts": "revision"}, "status": {"ts": "status"}}, "required": ["attempt", "deployment_id", "revision", "status"]};

import type { Codec } from '../codec-runtime';

export const codec_FailedCleanup: Codec = {"kind": "object", "fields": {"attempt": {"ts": "attempt"}, "deployment_id": {"ts": "deploymentId"}, "error": {"ts": "error"}, "error_code": {"ts": "errorCode"}, "review_id": {"ts": "reviewId"}, "review_sequence": {"ts": "reviewSequence"}, "revision": {"ts": "revision"}, "status": {"ts": "status"}, "variant": {"ts": "variant"}}, "required": ["attempt", "deployment_id", "revision", "status"]};

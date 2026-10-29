import type { Codec } from '../codec-runtime';

export const codec_ReleaseEvidence: Codec = {"kind": "object", "fields": {"deployment_id": {"ts": "deploymentId"}, "environment": {"ts": "environment"}, "reason": {"ts": "reason"}, "settled": {"ts": "settled"}, "status": {"ts": "status"}, "unverified": {"ts": "unverified"}, "verdict": {"ts": "verdict"}, "watch_id": {"ts": "watchId"}, "watched": {"ts": "watched"}}, "required": ["deployment_id", "environment", "settled", "unverified", "watched"]};

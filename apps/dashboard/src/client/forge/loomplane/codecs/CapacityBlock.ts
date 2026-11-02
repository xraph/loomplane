import type { Codec } from '../codec-runtime';

export const codec_CapacityBlock: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "counted": {"ts": "counted"}, "limit": {"ts": "limit"}, "policy_revision": {"ts": "policyRevision"}, "pool_id": {"ts": "poolId"}, "reason": {"ts": "reason"}, "requested": {"ts": "requested"}, "resource": {"ts": "resource"}, "retryable": {"ts": "retryable"}, "target_id": {"ts": "targetId"}}, "required": ["cloud_id", "counted", "limit", "policy_revision", "pool_id", "reason", "requested", "resource", "retryable", "target_id"]};

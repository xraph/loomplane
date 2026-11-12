import type { Codec } from '../codec-runtime';

export const codec_RolloutMemberPin: Codec = {"kind": "object", "fields": {"attempt": {"ts": "attempt"}, "dispatch_revision": {"ts": "dispatchRevision"}, "instance_id": {"ts": "instanceId"}, "rollout_id": {"ts": "rolloutId"}}, "required": ["attempt", "dispatch_revision", "instance_id", "rollout_id"]};

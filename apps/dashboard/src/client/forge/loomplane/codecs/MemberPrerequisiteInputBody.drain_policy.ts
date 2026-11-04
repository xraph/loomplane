import type { Codec } from '../codec-runtime';

export const codec_MemberPrerequisiteInputBody_drain_policy: Codec = {"kind": "object", "fields": {"max_connection_lifetime_seconds": {"ts": "maxConnectionLifetimeSeconds"}, "mode": {"ts": "mode"}, "termination_grace_seconds": {"ts": "terminationGraceSeconds"}}, "required": ["mode", "termination_grace_seconds"]};

import type { Codec } from '../codec-runtime';

export const codec_RetirementEvent: Codec = {"kind": "object", "fields": {"actor": {"ts": "actor"}, "attempt": {"ts": "attempt"}, "attempt_limit": {"ts": "attemptLimit"}, "created_at": {"ts": "createdAt"}, "cursor": {"ts": "cursor"}, "error": {"ts": "error"}, "kind": {"ts": "kind"}, "previous_actor": {"ts": "previousActor"}, "previous_error": {"ts": "previousError"}, "status": {"ts": "status"}}, "required": ["actor", "attempt", "attempt_limit", "created_at", "cursor", "kind", "status"]};

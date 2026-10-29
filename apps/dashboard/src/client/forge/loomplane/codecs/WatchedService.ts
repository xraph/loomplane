import type { Codec } from '../codec-runtime';

export const codec_WatchedService: Codec = {"kind": "object", "fields": {"binding_id": {"ts": "bindingId"}, "checked_at": {"ts": "checkedAt"}, "name": {"ts": "name"}, "op": {"ts": "op"}, "provider": {"ts": "provider"}, "reason": {"ts": "reason"}, "revision": {"ts": "revision"}, "verdict": {"ts": "verdict"}}, "required": ["binding_id", "name", "op", "provider", "reason", "revision", "verdict"]};

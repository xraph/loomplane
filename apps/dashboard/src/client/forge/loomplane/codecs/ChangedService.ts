import type { Codec } from '../codec-runtime';

export const codec_ChangedService: Codec = {"kind": "object", "fields": {"action_id": {"ts": "actionId"}, "canary": {"ts": "canary"}, "last_canary": {"ts": "lastCanary"}, "name": {"ts": "name"}, "op": {"ts": "op"}, "provider": {"ts": "provider"}}, "required": ["action_id", "canary", "last_canary", "name", "op", "provider"]};

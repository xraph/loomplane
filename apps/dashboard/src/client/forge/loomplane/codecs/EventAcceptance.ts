import type { Codec } from '../codec-runtime';

export const codec_EventAcceptance: Codec = {"kind": "object", "fields": {"attempts": {"ts": "attempts"}, "error_code": {"ts": "errorCode"}, "event_id": {"ts": "eventId"}, "state": {"ts": "state"}}, "required": ["attempts", "event_id", "state"]};

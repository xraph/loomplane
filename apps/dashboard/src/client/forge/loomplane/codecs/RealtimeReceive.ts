import type { Codec } from '../codec-runtime';

export const codec_RealtimeReceive: Codec = {"kind": "object", "fields": {"channel": {"ts": "channel"}, "cursor": {"ts": "cursor"}, "data": {"ts": "data"}, "event": {"ts": "event"}}, "required": ["channel", "cursor", "data", "event"]};

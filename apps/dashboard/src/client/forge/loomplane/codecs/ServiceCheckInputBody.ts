import type { Codec } from '../codec-runtime';

export const codec_ServiceCheckInputBody: Codec = {"kind": "object", "fields": {"dsn": {"ts": "dsn"}, "engine": {"ts": "engine"}, "target": {"ts": "target"}}, "required": ["dsn", "engine", "target"]};

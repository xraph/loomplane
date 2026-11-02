import type { Codec } from '../codec-runtime';

export const codec_CheckInputBody: Codec = {"kind": "object", "fields": {"dependency": {"ts": "dependency"}, "dsn": {"ts": "dsn"}, "engine": {"ts": "engine"}, "kind": {"ts": "kind"}, "target": {"ts": "target"}}, "required": ["dependency", "dsn", "engine", "kind", "target"]};

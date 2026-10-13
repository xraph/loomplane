import type { Codec } from '../codec-runtime';

export const codec_DriftCounts: Codec = {"kind": "object", "fields": {"changed": {"ts": "changed"}, "deployed": {"ts": "deployed"}, "missing": {"ts": "missing"}}, "required": ["changed", "deployed", "missing"]};

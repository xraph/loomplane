import type { Codec } from '../codec-runtime';

export const codec_HTTPProbe: Codec = {"kind": "object", "fields": {"checker": {"ts": "checker"}, "path": {"ts": "path"}, "port": {"ts": "port"}}, "required": ["path", "port"]};

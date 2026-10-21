import type { Codec } from '../codec-runtime';

export const codec_GatewayRoute: Codec = {"kind": "object", "fields": {"env": {"ts": "env"}, "path": {"ts": "path"}, "strip_prefix": {"ts": "stripPrefix"}, "to": {"ts": "to"}}, "required": ["env", "path", "to"]};

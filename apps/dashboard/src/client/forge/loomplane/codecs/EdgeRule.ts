import type { Codec } from '../codec-runtime';

export const codec_EdgeRule: Codec = {"kind": "object", "fields": {"browser_ttl": {"ts": "browserTtl"}, "edge_ttl": {"ts": "edgeTtl"}, "mode": {"ts": "mode"}, "path_prefix": {"ts": "pathPrefix"}}, "required": ["browser_ttl", "edge_ttl", "mode", "path_prefix"]};

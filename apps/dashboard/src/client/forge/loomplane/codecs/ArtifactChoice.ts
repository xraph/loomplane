import type { Codec } from '../codec-runtime';

export const codec_ArtifactChoice: Codec = {"kind": "object", "fields": {"from": {"ts": "from"}, "reason": {"ts": "reason"}, "reference": {"ts": "reference"}, "reused": {"ts": "reused"}, "revision": {"ts": "revision"}, "service": {"ts": "service"}}, "required": ["reused", "service"]};

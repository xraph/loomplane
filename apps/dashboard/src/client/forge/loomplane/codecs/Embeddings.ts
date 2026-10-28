import type { Codec } from '../codec-runtime';

export const codec_Embeddings: Codec = {"kind": "object", "fields": {"deployment": {"ts": "deployment"}, "dimensions": {"ts": "dimensions"}, "mode": {"ts": "mode"}, "model": {"ts": "model"}, "provider": {"ts": "provider"}}, "required": ["dimensions", "mode", "model"]};

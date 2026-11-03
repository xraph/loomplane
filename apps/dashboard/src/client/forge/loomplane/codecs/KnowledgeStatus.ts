import type { Codec } from '../codec-runtime';

export const codec_KnowledgeStatus: Codec = {"kind": "object", "fields": {"available": {"ts": "available"}, "configured": {"ts": "configured"}, "reason": {"ts": "reason"}}, "required": ["available", "configured"]};

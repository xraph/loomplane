import type { Codec } from '../codec-runtime';

export const codec_Exclusion: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "keep_running": {"ts": "keepRunning"}, "kind": {"ts": "kind"}, "match": {"ts": "match"}, "name": {"ts": "name"}, "reason": {"ts": "reason"}}, "required": ["created_at", "created_by", "keep_running", "kind", "match", "reason"]};

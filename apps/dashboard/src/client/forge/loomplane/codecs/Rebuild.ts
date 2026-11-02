import type { Codec } from '../codec-runtime';

export const codec_Rebuild: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "finished_at": {"ts": "finishedAt"}, "id": {"ts": "id"}, "output": {"ts": "output"}, "projection": {"ts": "projection"}, "retryable": {"ts": "retryable"}, "status": {"ts": "status"}}, "required": ["created_at", "created_by", "id", "output", "projection", "retryable", "status"]};

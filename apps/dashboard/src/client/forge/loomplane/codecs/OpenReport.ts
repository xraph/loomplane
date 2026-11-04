import type { Codec } from '../codec-runtime';

export const codec_OpenReport: Codec = {"kind": "object", "fields": {"id": {"ts": "id"}, "opened_at": {"ts": "openedAt"}, "path": {"ts": "path"}, "severity": {"ts": "severity"}, "title": {"ts": "title"}}, "required": ["id", "opened_at", "path", "title"]};

import type { Codec } from '../codec-runtime';

export const codec_Event: Codec = {"kind": "object", "fields": {"at": {"ts": "at"}, "data": {"ts": "data"}, "kind": {"ts": "kind"}, "seq": {"ts": "seq"}, "task_id": {"ts": "taskId"}}, "required": ["at", "data", "kind", "seq", "task_id"]};

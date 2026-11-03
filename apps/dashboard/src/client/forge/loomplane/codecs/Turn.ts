import type { Codec } from '../codec-runtime';
import { codec_Turn_events } from './Turn.events';

export const codec_Turn: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "error": {"ts": "error"}, "event_seq": {"ts": "eventSeq"}, "events": {"ts": "events", "codec": () => codec_Turn_events}, "finished_at": {"ts": "finishedAt"}, "message": {"ts": "message"}, "status": {"ts": "status"}, "task_id": {"ts": "taskId"}}, "required": ["created_at", "error", "event_seq", "events", "message", "status", "task_id"]};

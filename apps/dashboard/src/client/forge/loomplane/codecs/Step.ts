import type { Codec } from '../codec-runtime';
import { codec_Step_warnings } from './Step.warnings';

export const codec_Step: Codec = {"kind": "object", "fields": {"action_id": {"ts": "actionId"}, "finished_at": {"ts": "finishedAt"}, "image": {"ts": "image"}, "message": {"ts": "message"}, "name": {"ts": "name"}, "op": {"ts": "op"}, "order": {"ts": "order"}, "provider": {"ts": "provider"}, "revision": {"ts": "revision"}, "started_at": {"ts": "startedAt"}, "status": {"ts": "status"}, "subject": {"ts": "subject"}, "url": {"ts": "url"}, "warnings": {"ts": "warnings", "codec": () => codec_Step_warnings}}, "required": ["action_id", "image", "message", "name", "op", "order", "provider", "status", "subject", "url"]};

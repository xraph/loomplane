import type { Codec } from '../codec-runtime';
import { codec_StartInputBody_context } from './StartInputBody.context';

export const codec_StartInputBody: Codec = {"kind": "object", "fields": {"context": {"ts": "context", "codec": () => codec_StartInputBody_context}, "message": {"ts": "message"}, "project_id": {"ts": "projectId"}}, "required": ["message"]};

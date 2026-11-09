import type { Codec } from '../codec-runtime';
import { codec_SendInputBody_context } from './SendInputBody.context';

export const codec_SendInputBody: Codec = {"kind": "object", "fields": {"context": {"ts": "context", "codec": () => codec_SendInputBody_context}, "message": {"ts": "message"}}, "required": ["message"]};

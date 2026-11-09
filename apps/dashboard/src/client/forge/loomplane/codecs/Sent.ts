import type { Codec } from '../codec-runtime';
import { codec_Conversation } from './Conversation';
import { codec_Task } from './Task';

export const codec_Sent: Codec = {"kind": "object", "fields": {"conversation": {"ts": "conversation", "codec": () => codec_Conversation}, "task": {"ts": "task", "codec": () => codec_Task}}, "required": ["conversation", "task"]};

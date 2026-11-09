import type { Codec } from '../codec-runtime';
import { codec_Conversation } from './Conversation';
import { codec_History_turns } from './History.turns';

export const codec_History: Codec = {"kind": "object", "fields": {"conversation": {"ts": "conversation", "codec": () => codec_Conversation}, "more": {"ts": "more"}, "turns": {"ts": "turns", "codec": () => codec_History_turns}}, "required": ["conversation", "more", "turns"]};

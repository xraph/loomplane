import type { Codec } from '../codec-runtime';
import { codec_ConversationList_conversations } from './ConversationList.conversations';

export const codec_ConversationList: Codec = {"kind": "object", "fields": {"conversations": {"ts": "conversations", "codec": () => codec_ConversationList_conversations}}, "required": ["conversations"]};

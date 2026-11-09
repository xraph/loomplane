import type { Codec } from '../codec-runtime';
import { codec_Conversation } from './Conversation';

export const codec_ConversationList_conversations: Codec = {"kind": "array", "items": () => codec_Conversation};

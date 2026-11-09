import type { OperationMeta } from '../ops';
import { codec_Conversation } from '../codecs/Conversation';
import { codec_ConversationUpdateBody } from '../codecs/ConversationUpdateBody';

export const op_agentConversations_update = {
  method: 'PATCH',
  path: '/api/v1/agent/conversations/{id}',
  entity: 'Conversation',
  rootType: 'Conversation',
  provides: ['Conversation:{id}'],
  invalidates: ['Conversation[]'],
  bodyCodec: codec_ConversationUpdateBody,
  responseCodec: codec_Conversation,
} as const satisfies OperationMeta;

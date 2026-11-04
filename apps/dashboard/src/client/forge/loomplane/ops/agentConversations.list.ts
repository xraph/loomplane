import type { OperationMeta } from '../ops';
import { codec_ConversationList } from '../codecs/ConversationList';

export const op_agentConversations_list = {
  method: 'GET',
  path: '/api/v1/agent/conversations',
  entity: 'Conversation',
  rootType: 'ConversationList',
  provides: ['Conversation:{id}', 'Conversation[]'],
  invalidates: [],
  responseCodec: codec_ConversationList,
} as const satisfies OperationMeta;

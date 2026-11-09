import type { OperationMeta } from '../ops';

export const op_agentConversations_delete = {
  method: 'DELETE',
  path: '/api/v1/agent/conversations/{id}',
  provides: [],
  invalidates: ['Conversation[]'],
} as const satisfies OperationMeta;

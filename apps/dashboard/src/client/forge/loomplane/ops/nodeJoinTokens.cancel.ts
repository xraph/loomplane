import type { OperationMeta } from '../ops';

export const op_nodeJoinTokens_cancel = {
  method: 'DELETE',
  path: '/api/v1/nodes/join-tokens/{id}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

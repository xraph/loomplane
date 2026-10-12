import type { OperationMeta } from '../ops';

export const op_apiKeys_revoke = {
  method: 'DELETE',
  path: '/api/v1/api-keys/{id}',
  entity: 'APIKey',
  provides: [],
  invalidates: ['APIKey[]'],
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';

export const op_connections_delete = {
  method: 'DELETE',
  path: '/api/v1/connections/{id}',
  entity: 'Connection',
  provides: [],
  invalidates: ['Connection[]'],
} as const satisfies OperationMeta;

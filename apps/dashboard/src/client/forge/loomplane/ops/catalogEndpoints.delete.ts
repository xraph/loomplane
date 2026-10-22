import type { OperationMeta } from '../ops';

export const op_catalogEndpoints_delete = {
  method: 'DELETE',
  path: '/api/v1/catalog/endpoints/{id}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

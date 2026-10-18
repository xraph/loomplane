import type { OperationMeta } from '../ops';

export const op_serviceRestores_delete = {
  method: 'DELETE',
  path: '/api/v1/service-restores/{id}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

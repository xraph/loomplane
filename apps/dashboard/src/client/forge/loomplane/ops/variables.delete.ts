import type { OperationMeta } from '../ops';

export const op_variables_delete = {
  method: 'DELETE',
  path: '/api/v1/variables/{id}',
  entity: 'Variable',
  provides: [],
  invalidates: ['Variable[]'],
} as const satisfies OperationMeta;

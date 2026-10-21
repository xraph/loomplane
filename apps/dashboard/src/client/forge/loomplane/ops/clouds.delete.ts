import type { OperationMeta } from '../ops';

export const op_clouds_delete = {
  method: 'DELETE',
  path: '/api/v1/projects/{id}/clouds/{cloudId}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

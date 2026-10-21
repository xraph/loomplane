import type { OperationMeta } from '../ops';

export const op_cloudTargets_delete = {
  method: 'DELETE',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/targets/{targetId}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

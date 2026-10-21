import type { OperationMeta } from '../ops';

export const op_organizationCloudTargets_delete = {
  method: 'DELETE',
  path: '/api/v1/clouds/{cloudId}/targets/{targetId}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

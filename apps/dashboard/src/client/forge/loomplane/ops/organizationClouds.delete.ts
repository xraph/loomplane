import type { OperationMeta } from '../ops';

export const op_organizationClouds_delete = {
  method: 'DELETE',
  path: '/api/v1/clouds/{cloudId}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

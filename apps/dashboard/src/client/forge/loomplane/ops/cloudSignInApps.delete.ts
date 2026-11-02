import type { OperationMeta } from '../ops';

export const op_cloudSignInApps_delete = {
  method: 'DELETE',
  path: '/api/v1/cloud-sign-in/apps/{cloud}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

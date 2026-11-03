import type { OperationMeta } from '../ops';

export const op_platformCloudSignInApps_delete = {
  method: 'DELETE',
  path: '/api/v1/platform/cloud-sign-in/apps/{cloud}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

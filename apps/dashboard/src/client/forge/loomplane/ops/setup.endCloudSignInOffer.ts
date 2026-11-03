import type { OperationMeta } from '../ops';

export const op_setup_endCloudSignInOffer = {
  method: 'DELETE',
  path: '/api/v1/setup/cloud-sign-in-offer',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

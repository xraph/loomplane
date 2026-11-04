import type { OperationMeta } from '../ops';

export const op_platformCloudHosts_retire = {
  method: 'POST',
  path: '/api/v1/platform/cloud/hosts/{host}/retire',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

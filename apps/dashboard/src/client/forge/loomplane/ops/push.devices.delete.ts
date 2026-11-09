import type { OperationMeta } from '../ops';

export const op_push_devices_delete = {
  method: 'DELETE',
  path: '/api/v1/push/devices/{id}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

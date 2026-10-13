import type { OperationMeta } from '../ops';

export const op_realtime_ws = {
  method: 'GET',
  path: '/api/v1/realtime/ws',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';

export const op_realtime_events = {
  method: 'GET',
  path: '/api/v1/realtime/events',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';

export const op_runnerPools_delete = {
  method: 'DELETE',
  path: '/api/v1/runner-pools/{id}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

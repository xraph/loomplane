import type { OperationMeta } from '../ops';

export const op_agentTasks_stream = {
  method: 'GET',
  path: '/api/v1/agent/tasks/{id}/stream',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;

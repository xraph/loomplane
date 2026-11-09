import type { OperationMeta } from '../ops';
import { codec_Task } from '../codecs/Task';

export const op_agentTasks_get = {
  method: 'GET',
  path: '/api/v1/agent/tasks/{id}',
  entity: 'Task',
  rootType: 'Task',
  provides: ['Task:{id}'],
  invalidates: [],
  responseCodec: codec_Task,
} as const satisfies OperationMeta;

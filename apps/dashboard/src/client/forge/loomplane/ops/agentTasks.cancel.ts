import type { OperationMeta } from '../ops';
import { codec_Task } from '../codecs/Task';

export const op_agentTasks_cancel = {
  method: 'POST',
  path: '/api/v1/agent/tasks/{id}/cancel',
  entity: 'Task',
  rootType: 'Task',
  provides: ['Task:{id}'],
  invalidates: ['Task[]'],
  responseCodec: codec_Task,
} as const satisfies OperationMeta;

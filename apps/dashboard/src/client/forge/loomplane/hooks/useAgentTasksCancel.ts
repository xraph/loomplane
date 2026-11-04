import { mutation } from '@forge-go/client-core';
import { op_agentTasks_cancel } from '../ops/agentTasks.cancel';
import type { Task } from '../types';

export const useAgentTasksCancel = /*#__PURE__*/ mutation<Task, Task>(op_agentTasks_cancel);

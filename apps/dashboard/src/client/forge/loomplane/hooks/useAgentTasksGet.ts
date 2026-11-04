import { query } from '@forge-go/client-core';
import { op_agentTasks_get } from '../ops/agentTasks.get';
import type { Task } from '../types';

export const useAgentTasksGet = /*#__PURE__*/ query<Task>(op_agentTasks_get);

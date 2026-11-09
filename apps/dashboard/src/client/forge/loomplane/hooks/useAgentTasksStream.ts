import { query } from '@forge-go/client-core';
import { op_agentTasks_stream } from '../ops/agentTasks.stream';

export const useAgentTasksStream = /*#__PURE__*/ query(op_agentTasks_stream);

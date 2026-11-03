import { query } from '@forge-go/client-core';
import { op_agentStatus_get } from '../ops/agentStatus.get';
import type { AgentsettingsStatus } from '../types';

export const useAgentStatusGet = /*#__PURE__*/ query<AgentsettingsStatus>(op_agentStatus_get);

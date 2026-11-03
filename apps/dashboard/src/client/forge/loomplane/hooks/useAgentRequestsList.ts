import { query } from '@forge-go/client-core';
import { op_agentRequests_list } from '../ops/agentRequests.list';
import type { RequestList } from '../types';

export const useAgentRequestsList = /*#__PURE__*/ query<RequestList>(op_agentRequests_list);

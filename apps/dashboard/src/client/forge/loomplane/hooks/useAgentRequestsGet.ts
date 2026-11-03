import { query } from '@forge-go/client-core';
import { op_agentRequests_get } from '../ops/agentRequests.get';
import type { Request } from '../types';

export const useAgentRequestsGet = /*#__PURE__*/ query<Request>(op_agentRequests_get);

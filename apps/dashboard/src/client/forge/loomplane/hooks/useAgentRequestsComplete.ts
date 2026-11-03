import { mutation } from '@forge-go/client-core';
import { op_agentRequests_complete } from '../ops/agentRequests.complete';
import type { Request } from '../types';

export const useAgentRequestsComplete = /*#__PURE__*/ mutation<Request, Request>(op_agentRequests_complete);

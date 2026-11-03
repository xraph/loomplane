import { mutation } from '@forge-go/client-core';
import { op_agentRequests_decline } from '../ops/agentRequests.decline';
import type { Request } from '../types';

export const useAgentRequestsDecline = /*#__PURE__*/ mutation<Request, Request>(op_agentRequests_decline);

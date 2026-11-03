import { mutation } from '@forge-go/client-core';
import { op_agentRequests_answer } from '../ops/agentRequests.answer';
import type { Request } from '../types';

export const useAgentRequestsAnswer = /*#__PURE__*/ mutation<Request, Request>(op_agentRequests_answer);

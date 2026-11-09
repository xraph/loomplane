import { mutation } from '@forge-go/client-core';
import { op_agentRequests_provideSecret } from '../ops/agentRequests.provideSecret';
import type { Request } from '../types';

export const useAgentRequestsProvideSecret = /*#__PURE__*/ mutation<Request, Request>(op_agentRequests_provideSecret);

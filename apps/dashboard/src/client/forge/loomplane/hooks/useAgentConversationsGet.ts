import { query } from '@forge-go/client-core';
import { op_agentConversations_get } from '../ops/agentConversations.get';
import type { History } from '../types';

export const useAgentConversationsGet = /*#__PURE__*/ query<History>(op_agentConversations_get);

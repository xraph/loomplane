import { query } from '@forge-go/client-core';
import { op_agentConversations_list } from '../ops/agentConversations.list';
import type { ConversationList } from '../types';

export const useAgentConversationsList = /*#__PURE__*/ query<ConversationList>(op_agentConversations_list);

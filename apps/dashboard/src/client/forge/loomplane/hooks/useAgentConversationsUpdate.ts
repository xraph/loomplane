import { mutation } from '@forge-go/client-core';
import { op_agentConversations_update } from '../ops/agentConversations.update';
import type { Conversation } from '../types';

export const useAgentConversationsUpdate = /*#__PURE__*/ mutation<Conversation, Conversation>(op_agentConversations_update);

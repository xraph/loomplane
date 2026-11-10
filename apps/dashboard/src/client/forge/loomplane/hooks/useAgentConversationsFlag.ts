import { mutation } from '@forge-go/client-core';
import { op_agentConversations_flag } from '../ops/agentConversations.flag';
import type { Flag } from '../types';

export const useAgentConversationsFlag = /*#__PURE__*/ mutation<Flag, Flag>(op_agentConversations_flag);

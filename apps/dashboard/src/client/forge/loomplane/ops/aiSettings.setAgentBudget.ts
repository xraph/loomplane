import type { OperationMeta } from '../ops';
import { codec_AgentBudgetInputBody } from '../codecs/AgentBudgetInputBody';
import { codec_AgentView } from '../codecs/AgentView';

export const op_aiSettings_setAgentBudget = {
  method: 'PUT',
  path: '/api/v1/ai/settings/agent/budget',
  provides: [],
  invalidates: [],
  bodyCodec: codec_AgentBudgetInputBody,
  responseCodec: codec_AgentView,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_AgentUpdateInputBody } from '../codecs/AgentUpdateInputBody';
import { codec_AgentView } from '../codecs/AgentView';

export const op_aiSettings_updateAgent = {
  method: 'PATCH',
  path: '/api/v1/ai/settings/agent',
  provides: [],
  invalidates: [],
  bodyCodec: codec_AgentUpdateInputBody,
  responseCodec: codec_AgentView,
} as const satisfies OperationMeta;

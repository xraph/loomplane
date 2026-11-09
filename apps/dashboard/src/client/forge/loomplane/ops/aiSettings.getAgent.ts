import type { OperationMeta } from '../ops';
import { codec_AgentView } from '../codecs/AgentView';

export const op_aiSettings_getAgent = {
  method: 'GET',
  path: '/api/v1/ai/settings/agent',
  provides: [],
  invalidates: [],
  responseCodec: codec_AgentView,
} as const satisfies OperationMeta;

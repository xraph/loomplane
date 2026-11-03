import type { OperationMeta } from '../ops';
import { codec_AgentsettingsStatus } from '../codecs/AgentsettingsStatus';

export const op_agentStatus_get = {
  method: 'GET',
  path: '/api/v1/agent/status',
  provides: [],
  invalidates: [],
  responseCodec: codec_AgentsettingsStatus,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_ServerUsage } from '../codecs/ServerUsage';

export const op_cloudServers_usage = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/servers/{serverId}/usage',
  provides: [],
  invalidates: [],
  responseCodec: codec_ServerUsage,
} as const satisfies OperationMeta;

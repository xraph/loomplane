import type { OperationMeta } from '../ops';
import { codec_Server } from '../codecs/Server';

export const op_cloudServers_check = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/servers/{serverId}/check',
  entity: 'Server',
  rootType: 'Server',
  provides: ['Server:{id}'],
  invalidates: ['Server[]'],
  responseCodec: codec_Server,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_ApplyInputBody } from '../codecs/ApplyInputBody';
import { codec_Server } from '../codecs/Server';

export const op_cloudServers_applyDrain = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/servers/{serverId}/drain/apply',
  entity: 'Server',
  rootType: 'Server',
  provides: ['Server:{id}'],
  invalidates: ['Server[]'],
  bodyCodec: codec_ApplyInputBody,
  responseCodec: codec_Server,
} as const satisfies OperationMeta;

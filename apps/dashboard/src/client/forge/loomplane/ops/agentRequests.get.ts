import type { OperationMeta } from '../ops';
import { codec_Request } from '../codecs/Request';

export const op_agentRequests_get = {
  method: 'GET',
  path: '/api/v1/agent/requests/{id}',
  entity: 'Request',
  rootType: 'Request',
  provides: ['Request:{id}'],
  invalidates: [],
  responseCodec: codec_Request,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_Request } from '../codecs/Request';

export const op_agentRequests_complete = {
  method: 'POST',
  path: '/api/v1/agent/requests/{id}/complete',
  entity: 'Request',
  rootType: 'Request',
  provides: ['Request:{id}'],
  invalidates: ['Request[]'],
  responseCodec: codec_Request,
} as const satisfies OperationMeta;

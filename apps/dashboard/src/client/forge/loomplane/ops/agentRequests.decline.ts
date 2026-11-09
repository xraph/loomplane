import type { OperationMeta } from '../ops';
import { codec_DeclineInputBody } from '../codecs/DeclineInputBody';
import { codec_Request } from '../codecs/Request';

export const op_agentRequests_decline = {
  method: 'POST',
  path: '/api/v1/agent/requests/{id}/decline',
  entity: 'Request',
  rootType: 'Request',
  provides: ['Request:{id}'],
  invalidates: ['Request[]'],
  bodyCodec: codec_DeclineInputBody,
  responseCodec: codec_Request,
} as const satisfies OperationMeta;

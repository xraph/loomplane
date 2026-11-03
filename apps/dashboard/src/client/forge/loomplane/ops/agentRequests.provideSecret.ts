import type { OperationMeta } from '../ops';
import { codec_Request } from '../codecs/Request';
import { codec_SecretInputBody } from '../codecs/SecretInputBody';

export const op_agentRequests_provideSecret = {
  method: 'POST',
  path: '/api/v1/agent/requests/{id}/secret',
  entity: 'Request',
  rootType: 'Request',
  provides: ['Request:{id}'],
  invalidates: ['Request[]'],
  bodyCodec: codec_SecretInputBody,
  responseCodec: codec_Request,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_AnswerInputBody } from '../codecs/AnswerInputBody';
import { codec_Request } from '../codecs/Request';

export const op_agentRequests_answer = {
  method: 'POST',
  path: '/api/v1/agent/requests/{id}/answer',
  entity: 'Request',
  rootType: 'Request',
  provides: ['Request:{id}'],
  invalidates: ['Request[]'],
  bodyCodec: codec_AnswerInputBody,
  responseCodec: codec_Request,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_SendInputBody } from '../codecs/SendInputBody';
import { codec_Sent } from '../codecs/Sent';

export const op_agentConversations_send = {
  method: 'POST',
  path: '/api/v1/agent/conversations/{id}/messages',
  rootType: 'Sent',
  provides: [],
  invalidates: [],
  bodyCodec: codec_SendInputBody,
  responseCodec: codec_Sent,
} as const satisfies OperationMeta;

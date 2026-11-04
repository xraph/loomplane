import type { OperationMeta } from '../ops';
import { codec_Sent } from '../codecs/Sent';
import { codec_StartInputBody } from '../codecs/StartInputBody';

export const op_agentConversations_start = {
  method: 'POST',
  path: '/api/v1/agent/conversations',
  rootType: 'Sent',
  provides: [],
  invalidates: [],
  bodyCodec: codec_StartInputBody,
  responseCodec: codec_Sent,
} as const satisfies OperationMeta;

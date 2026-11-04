import type { OperationMeta } from '../ops';
import { codec_History } from '../codecs/History';

export const op_agentConversations_get = {
  method: 'GET',
  path: '/api/v1/agent/conversations/{id}',
  rootType: 'History',
  provides: [],
  invalidates: [],
  responseCodec: codec_History,
} as const satisfies OperationMeta;

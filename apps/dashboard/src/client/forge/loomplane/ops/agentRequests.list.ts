import type { OperationMeta } from '../ops';
import { codec_RequestList } from '../codecs/RequestList';

export const op_agentRequests_list = {
  method: 'GET',
  path: '/api/v1/agent/requests',
  provides: [],
  invalidates: [],
  responseCodec: codec_RequestList,
} as const satisfies OperationMeta;

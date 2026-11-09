import type { OperationMeta } from '../ops';
import { codec_JoinTokenList } from '../codecs/JoinTokenList';

export const op_nodeJoinTokens_list = {
  method: 'GET',
  path: '/api/v1/nodes/join-tokens',
  entity: 'PendingNodeJoinToken',
  rootType: 'JoinTokenList',
  provides: ['PendingNodeJoinToken:{id}', 'PendingNodeJoinToken[]'],
  invalidates: [],
  responseCodec: codec_JoinTokenList,
} as const satisfies OperationMeta;

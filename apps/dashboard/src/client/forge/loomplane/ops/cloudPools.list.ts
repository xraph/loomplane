import type { OperationMeta } from '../ops';
import { codec_PoolList } from '../codecs/PoolList';

export const op_cloudPools_list = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/pools',
  entity: 'Pool',
  rootType: 'PoolList',
  provides: ['Pool:{id}', 'Pool[]'],
  invalidates: [],
  responseCodec: codec_PoolList,
} as const satisfies OperationMeta;

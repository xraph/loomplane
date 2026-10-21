import type { OperationMeta } from '../ops';
import { codec_Pool } from '../codecs/Pool';
import { codec_PoolInputBody } from '../codecs/PoolInputBody';

export const op_cloudPools_create = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/pools',
  entity: 'Pool',
  rootType: 'Pool',
  provides: ['Pool:{id}'],
  invalidates: ['Pool[]'],
  bodyCodec: codec_PoolInputBody,
  responseCodec: codec_Pool,
} as const satisfies OperationMeta;

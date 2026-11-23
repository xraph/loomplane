import type { OperationMeta } from '../ops';
import { codec_RunnersPoolList } from '../codecs/RunnersPoolList';

export const op_platformRunnerPools_list = {
  method: 'GET',
  path: '/api/v1/platform/runner-pools',
  entity: 'PoolView',
  rootType: 'RunnersPoolList',
  provides: ['PoolView:{id}', 'PoolView[]'],
  invalidates: [],
  responseCodec: codec_RunnersPoolList,
} as const satisfies OperationMeta;

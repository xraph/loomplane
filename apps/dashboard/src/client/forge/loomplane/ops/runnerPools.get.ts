import type { OperationMeta } from '../ops';
import { codec_PoolView } from '../codecs/PoolView';

export const op_runnerPools_get = {
  method: 'GET',
  path: '/api/v1/runner-pools/{id}',
  entity: 'PoolView',
  rootType: 'PoolView',
  provides: ['PoolView:{id}'],
  invalidates: [],
  responseCodec: codec_PoolView,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_BuildrunnerPoolInputBody } from '../codecs/BuildrunnerPoolInputBody';
import { codec_PoolView } from '../codecs/PoolView';

export const op_platformRunnerPools_create = {
  method: 'POST',
  path: '/api/v1/platform/runner-pools',
  entity: 'PoolView',
  rootType: 'PoolView',
  provides: ['PoolView:{id}'],
  invalidates: ['PoolView[]'],
  bodyCodec: codec_BuildrunnerPoolInputBody,
  responseCodec: codec_PoolView,
} as const satisfies OperationMeta;

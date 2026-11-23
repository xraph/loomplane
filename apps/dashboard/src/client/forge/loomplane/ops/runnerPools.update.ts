import type { OperationMeta } from '../ops';
import { codec_PoolSettingsBody } from '../codecs/PoolSettingsBody';
import { codec_PoolView } from '../codecs/PoolView';

export const op_runnerPools_update = {
  method: 'PATCH',
  path: '/api/v1/runner-pools/{id}',
  entity: 'PoolView',
  rootType: 'PoolView',
  provides: ['PoolView:{id}'],
  invalidates: ['PoolView[]'],
  bodyCodec: codec_PoolSettingsBody,
  responseCodec: codec_PoolView,
} as const satisfies OperationMeta;

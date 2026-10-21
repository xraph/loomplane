import type { OperationMeta } from '../ops';
import { codec_Target } from '../codecs/Target';

export const op_organizationCloudTargets_get = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/targets/{targetId}',
  entity: 'Target',
  rootType: 'Target',
  provides: ['Target:{id}'],
  invalidates: [],
  responseCodec: codec_Target,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_Target } from '../codecs/Target';
import { codec_UpdateTargetInputBody } from '../codecs/UpdateTargetInputBody';

export const op_cloudTargets_update = {
  method: 'PUT',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/targets/{targetId}',
  entity: 'Target',
  rootType: 'Target',
  provides: ['Target:{id}'],
  invalidates: ['Target[]'],
  bodyCodec: codec_UpdateTargetInputBody,
  responseCodec: codec_Target,
} as const satisfies OperationMeta;

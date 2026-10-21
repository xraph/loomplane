import type { OperationMeta } from '../ops';
import { codec_CreateTargetInputBody } from '../codecs/CreateTargetInputBody';
import { codec_Target } from '../codecs/Target';

export const op_cloudTargets_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/targets',
  entity: 'Target',
  rootType: 'Target',
  provides: ['Target:{id}'],
  invalidates: ['Target[]'],
  bodyCodec: codec_CreateTargetInputBody,
  responseCodec: codec_Target,
} as const satisfies OperationMeta;

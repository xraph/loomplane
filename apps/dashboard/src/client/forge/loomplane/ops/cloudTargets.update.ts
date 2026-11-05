import type { OperationMeta } from '../ops';
import { codec_CloudprofilesTarget } from '../codecs/CloudprofilesTarget';
import { codec_UpdateTargetInputBody } from '../codecs/UpdateTargetInputBody';

export const op_cloudTargets_update = {
  method: 'PUT',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/targets/{targetId}',
  entity: 'CloudprofilesTarget',
  rootType: 'CloudprofilesTarget',
  provides: ['CloudprofilesTarget:{id}'],
  invalidates: ['CloudprofilesTarget[]'],
  bodyCodec: codec_UpdateTargetInputBody,
  responseCodec: codec_CloudprofilesTarget,
} as const satisfies OperationMeta;

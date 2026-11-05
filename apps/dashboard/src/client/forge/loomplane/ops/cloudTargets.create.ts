import type { OperationMeta } from '../ops';
import { codec_CloudprofilesTarget } from '../codecs/CloudprofilesTarget';
import { codec_CreateTargetInputBody } from '../codecs/CreateTargetInputBody';

export const op_cloudTargets_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/targets',
  entity: 'CloudprofilesTarget',
  rootType: 'CloudprofilesTarget',
  provides: ['CloudprofilesTarget:{id}'],
  invalidates: ['CloudprofilesTarget[]'],
  bodyCodec: codec_CreateTargetInputBody,
  responseCodec: codec_CloudprofilesTarget,
} as const satisfies OperationMeta;

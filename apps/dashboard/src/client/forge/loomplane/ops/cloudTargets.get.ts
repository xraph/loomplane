import type { OperationMeta } from '../ops';
import { codec_CloudprofilesTarget } from '../codecs/CloudprofilesTarget';

export const op_cloudTargets_get = {
  method: 'GET',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/targets/{targetId}',
  entity: 'CloudprofilesTarget',
  rootType: 'CloudprofilesTarget',
  provides: ['CloudprofilesTarget:{id}'],
  invalidates: [],
  responseCodec: codec_CloudprofilesTarget,
} as const satisfies OperationMeta;

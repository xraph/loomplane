import type { OperationMeta } from '../ops';
import { codec_CloudprofilesTarget } from '../codecs/CloudprofilesTarget';

export const op_organizationCloudTargets_get = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/targets/{targetId}',
  entity: 'CloudprofilesTarget',
  rootType: 'CloudprofilesTarget',
  provides: ['CloudprofilesTarget:{id}'],
  invalidates: [],
  responseCodec: codec_CloudprofilesTarget,
} as const satisfies OperationMeta;

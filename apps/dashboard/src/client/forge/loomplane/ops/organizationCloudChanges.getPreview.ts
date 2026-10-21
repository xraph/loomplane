import type { OperationMeta } from '../ops';
import { codec_Preview } from '../codecs/Preview';

export const op_organizationCloudChanges_getPreview = {
  method: 'GET',
  path: '/api/v1/clouds/changes/{previewId}',
  entity: 'Preview',
  rootType: 'Preview',
  provides: ['Preview:{id}'],
  invalidates: [],
  responseCodec: codec_Preview,
} as const satisfies OperationMeta;

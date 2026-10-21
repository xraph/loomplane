import type { OperationMeta } from '../ops';
import { codec_Preview } from '../codecs/Preview';

export const op_organizationCloudSyncs_previewRevert = {
  method: 'POST',
  path: '/api/v1/clouds/syncs/{syncId}/revert/preview',
  entity: 'Preview',
  rootType: 'Preview',
  provides: ['Preview:{id}'],
  invalidates: ['Preview[]'],
  responseCodec: codec_Preview,
} as const satisfies OperationMeta;

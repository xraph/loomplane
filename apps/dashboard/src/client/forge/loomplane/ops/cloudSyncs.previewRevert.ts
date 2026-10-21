import type { OperationMeta } from '../ops';
import { codec_Preview } from '../codecs/Preview';

export const op_cloudSyncs_previewRevert = {
  method: 'POST',
  path: '/api/v1/projects/{id}/cloud-syncs/{syncId}/revert/preview',
  entity: 'Preview',
  rootType: 'Preview',
  provides: ['Preview:{id}'],
  invalidates: ['Preview[]'],
  responseCodec: codec_Preview,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_DrainReview } from '../codecs/DrainReview';

export const op_cloudServers_previewDrain = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/servers/{serverId}/drain/preview',
  provides: [],
  invalidates: [],
  responseCodec: codec_DrainReview,
} as const satisfies OperationMeta;

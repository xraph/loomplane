import type { OperationMeta } from '../ops';
import { codec_CloudinfraMoveInputBody } from '../codecs/CloudinfraMoveInputBody';
import { codec_MoveReview } from '../codecs/MoveReview';

export const op_cloudPlacements_previewMove = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/placements/preview-move',
  provides: [],
  invalidates: [],
  bodyCodec: codec_CloudinfraMoveInputBody,
  responseCodec: codec_MoveReview,
} as const satisfies OperationMeta;

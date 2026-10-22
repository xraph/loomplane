import type { OperationMeta } from '../ops';
import { codec_MoveInputBody } from '../codecs/MoveInputBody';
import { codec_MoveReview } from '../codecs/MoveReview';

export const op_cloudPlacements_acceptMove = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/placements/accept-move',
  provides: [],
  invalidates: [],
  bodyCodec: codec_MoveInputBody,
  responseCodec: codec_MoveReview,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_NativeCopyReview } from '../codecs/NativeCopyReview';
import { codec_NativeCopyReviewInputBody } from '../codecs/NativeCopyReviewInputBody';

export const op_nativeRecoveryCopies_review = {
  method: 'POST',
  path: '/api/v1/native-recovery-sources/{id}/points/{pointId}/copy-review',
  provides: [],
  invalidates: [],
  bodyCodec: codec_NativeCopyReviewInputBody,
  responseCodec: codec_NativeCopyReview,
} as const satisfies OperationMeta;

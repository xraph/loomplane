import type { OperationMeta } from '../ops';
import { codec_CleanupReview } from '../codecs/CleanupReview';

export const op_deployments_cleanupPreview = {
  method: 'POST',
  path: '/api/v1/deployments/{id}/cleanup/preview',
  provides: [],
  invalidates: [],
  responseCodec: codec_CleanupReview,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_FailedCleanup } from '../codecs/FailedCleanup';
import { codec_FailedCleanupInputBody } from '../codecs/FailedCleanupInputBody';

export const op_deployments_cleanupApply = {
  method: 'POST',
  path: '/api/v1/deployments/{id}/cleanup/apply',
  provides: [],
  invalidates: [],
  bodyCodec: codec_FailedCleanupInputBody,
  responseCodec: codec_FailedCleanup,
} as const satisfies OperationMeta;

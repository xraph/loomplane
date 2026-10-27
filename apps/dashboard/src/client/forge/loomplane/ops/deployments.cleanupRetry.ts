import type { OperationMeta } from '../ops';
import { codec_FailedCleanup } from '../codecs/FailedCleanup';

export const op_deployments_cleanupRetry = {
  method: 'POST',
  path: '/api/v1/deployments/{id}/cleanup/retry',
  provides: [],
  invalidates: [],
  responseCodec: codec_FailedCleanup,
} as const satisfies OperationMeta;

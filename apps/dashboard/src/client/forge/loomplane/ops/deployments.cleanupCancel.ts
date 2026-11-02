import type { OperationMeta } from '../ops';
import { codec_FailedCleanup } from '../codecs/FailedCleanup';
import { codec_FailedCleanupCancelInputBody } from '../codecs/FailedCleanupCancelInputBody';

export const op_deployments_cleanupCancel = {
  method: 'POST',
  path: '/api/v1/deployments/{id}/cleanup/cancel',
  provides: [],
  invalidates: [],
  bodyCodec: codec_FailedCleanupCancelInputBody,
  responseCodec: codec_FailedCleanup,
} as const satisfies OperationMeta;

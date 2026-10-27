import type { OperationMeta } from '../ops';
import { codec_FailedCleanup } from '../codecs/FailedCleanup';

export const op_deployments_cleanup = {
  method: 'GET',
  path: '/api/v1/deployments/{id}/cleanup',
  provides: [],
  invalidates: [],
  responseCodec: codec_FailedCleanup,
} as const satisfies OperationMeta;

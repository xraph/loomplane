import type { OperationMeta } from '../ops';
import { codec_OK } from '../codecs/OK';

export const op_notifications_retry = {
  method: 'POST',
  path: '/api/v1/notifications/retry',
  provides: [],
  invalidates: [],
  responseCodec: codec_OK,
} as const satisfies OperationMeta;

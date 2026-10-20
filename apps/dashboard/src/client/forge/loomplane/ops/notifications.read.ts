import type { OperationMeta } from '../ops';
import { codec_OK } from '../codecs/OK';

export const op_notifications_read = {
  method: 'PUT',
  path: '/api/v1/notifications/{id}/read',
  provides: [],
  invalidates: [],
  responseCodec: codec_OK,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_OK } from '../codecs/OK';

export const op_notifications_webhooks_delete = {
  method: 'DELETE',
  path: '/api/v1/notifications/webhooks/{id}',
  provides: [],
  invalidates: [],
  responseCodec: codec_OK,
} as const satisfies OperationMeta;

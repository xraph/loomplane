import type { OperationMeta } from '../ops';
import { codec_WebhookEndpointSecret } from '../codecs/WebhookEndpointSecret';

export const op_notifications_webhooks_rotate = {
  method: 'POST',
  path: '/api/v1/notifications/webhooks/{id}/rotate',
  rootType: 'WebhookEndpointSecret',
  provides: [],
  invalidates: [],
  responseCodec: codec_WebhookEndpointSecret,
} as const satisfies OperationMeta;

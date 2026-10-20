import type { OperationMeta } from '../ops';
import { codec_WebhookEndpointInputBody } from '../codecs/WebhookEndpointInputBody';
import { codec_WebhookEndpointSecret } from '../codecs/WebhookEndpointSecret';

export const op_notifications_webhooks_create = {
  method: 'POST',
  path: '/api/v1/notifications/webhooks',
  rootType: 'WebhookEndpointSecret',
  provides: [],
  invalidates: [],
  bodyCodec: codec_WebhookEndpointInputBody,
  responseCodec: codec_WebhookEndpointSecret,
} as const satisfies OperationMeta;

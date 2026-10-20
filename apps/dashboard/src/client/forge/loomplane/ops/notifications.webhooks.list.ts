import type { OperationMeta } from '../ops';
import { codec_WebhookEndpointList } from '../codecs/WebhookEndpointList';

export const op_notifications_webhooks_list = {
  method: 'GET',
  path: '/api/v1/notifications/webhooks',
  entity: 'WebhookEndpoint',
  rootType: 'WebhookEndpointList',
  provides: ['WebhookEndpoint:{id}', 'WebhookEndpoint[]'],
  invalidates: [],
  responseCodec: codec_WebhookEndpointList,
} as const satisfies OperationMeta;

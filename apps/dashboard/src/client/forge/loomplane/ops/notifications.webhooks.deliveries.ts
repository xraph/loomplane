import type { OperationMeta } from '../ops';
import { codec_DeliveryList } from '../codecs/DeliveryList';

export const op_notifications_webhooks_deliveries = {
  method: 'GET',
  path: '/api/v1/notifications/webhooks/{id}/deliveries',
  entity: 'Delivery',
  rootType: 'DeliveryList',
  provides: ['Delivery:{id}', 'Delivery[]'],
  invalidates: [],
  responseCodec: codec_DeliveryList,
} as const satisfies OperationMeta;

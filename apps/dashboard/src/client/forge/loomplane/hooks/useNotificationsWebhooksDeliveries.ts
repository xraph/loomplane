import { query } from '@forge-go/client-core';
import { op_notifications_webhooks_deliveries } from '../ops/notifications.webhooks.deliveries';
import type { DeliveryList } from '../types';

export const useNotificationsWebhooksDeliveries = /*#__PURE__*/ query<DeliveryList>(op_notifications_webhooks_deliveries);

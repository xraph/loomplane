import { query } from '@forge-go/client-core';
import { op_notifications_webhooks_list } from '../ops/notifications.webhooks.list';
import type { WebhookEndpointList } from '../types';

export const useNotificationsWebhooksList = /*#__PURE__*/ query<WebhookEndpointList>(op_notifications_webhooks_list);

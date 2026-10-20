import { query } from '@forge-go/client-core';
import { op_notifications_list } from '../ops/notifications.list';
import type { NotificationList } from '../types';

export const useNotificationsList = /*#__PURE__*/ query<NotificationList>(op_notifications_list);

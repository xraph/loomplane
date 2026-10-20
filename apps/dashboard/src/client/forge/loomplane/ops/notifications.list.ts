import type { OperationMeta } from '../ops';
import { codec_NotificationList } from '../codecs/NotificationList';

export const op_notifications_list = {
  method: 'GET',
  path: '/api/v1/notifications',
  entity: 'Notification',
  rootType: 'NotificationList',
  provides: ['Notification:{id}', 'Notification[]'],
  invalidates: [],
  responseCodec: codec_NotificationList,
} as const satisfies OperationMeta;

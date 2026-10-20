import type { Codec } from '../codec-runtime';
import { codec_Notification } from './Notification';

export const codec_NotificationList_items: Codec = {"kind": "array", "items": () => codec_Notification};

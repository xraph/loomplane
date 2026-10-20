import type { Codec } from '../codec-runtime';
import { codec_NotificationList_items } from './NotificationList.items';

export const codec_NotificationList: Codec = {"kind": "object", "fields": {"items": {"ts": "items", "codec": () => codec_NotificationList_items}, "mail_enabled": {"ts": "mailEnabled"}}, "required": ["items", "mail_enabled"]};

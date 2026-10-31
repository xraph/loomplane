import type { Codec } from '../codec-runtime';
import { codec_PushDevice_settings } from './PushDevice.settings';
import { codec_QuietHours } from './QuietHours';

export const codec_PushDevice: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "id": {"ts": "id"}, "last_seen_at": {"ts": "lastSeenAt"}, "name": {"ts": "name"}, "platform": {"ts": "platform"}, "quiet_hours": {"ts": "quietHours", "codec": () => codec_QuietHours}, "settings": {"ts": "settings", "codec": () => codec_PushDevice_settings}, "time_zone": {"ts": "timeZone"}}, "required": ["created_at", "id", "last_seen_at", "name", "platform", "settings", "time_zone"]};

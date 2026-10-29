import type { Codec } from '../codec-runtime';
import { codec_QuietHours } from './QuietHours';
import { codec_UpdateDeviceInputBody_settings } from './UpdateDeviceInputBody.settings';

export const codec_UpdateDeviceInputBody: Codec = {"kind": "object", "fields": {"quiet_hours": {"ts": "quietHours", "codec": () => codec_QuietHours}, "settings": {"ts": "settings", "codec": () => codec_UpdateDeviceInputBody_settings}, "time_zone": {"ts": "timeZone"}}};

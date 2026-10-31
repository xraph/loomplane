import type { Codec } from '../codec-runtime';
import { codec_UpdateDeviceInputBody_quiet_hours } from './UpdateDeviceInputBody.quiet_hours';
import { codec_UpdateDeviceInputBody_settings } from './UpdateDeviceInputBody.settings';

export const codec_UpdateDeviceInputBody: Codec = {"kind": "object", "fields": {"quiet_hours": {"ts": "quietHours", "codec": () => codec_UpdateDeviceInputBody_quiet_hours}, "settings": {"ts": "settings", "codec": () => codec_UpdateDeviceInputBody_settings}, "time_zone": {"ts": "timeZone"}}, "required": ["quiet_hours"]};

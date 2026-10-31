import type { Codec } from '../codec-runtime';
import { codec_QuietHours } from './QuietHours';

export const codec_UpdateDeviceInputBody_quiet_hours: Codec = {"kind": "object", "fields": {"Set": {"ts": "set"}, "Value": {"ts": "value", "codec": () => codec_QuietHours}}, "required": ["Set"]};

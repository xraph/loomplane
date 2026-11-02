import type { Codec } from '../codec-runtime';
import { codec_RegisterDeviceInputBody_settings } from './RegisterDeviceInputBody.settings';

export const codec_RegisterDeviceInputBody: Codec = {"kind": "object", "fields": {"handle": {"ts": "handle"}, "name": {"ts": "name"}, "platform": {"ts": "platform"}, "public_key": {"ts": "publicKey"}, "settings": {"ts": "settings", "codec": () => codec_RegisterDeviceInputBody_settings}, "time_zone": {"ts": "timeZone"}}, "required": ["handle", "name", "platform", "public_key", "settings", "time_zone"]};

import type { Codec } from '../codec-runtime';
import { codec_KeylessInputBody_settings } from './KeylessInputBody.settings';

export const codec_KeylessInputBody: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "name": {"ts": "name"}, "settings": {"ts": "settings", "codec": () => codec_KeylessInputBody_settings}}, "required": ["connection_id", "name", "settings"]};

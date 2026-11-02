import type { Codec } from '../codec-runtime';
import { codec_SignInInputBody_settings } from './SignInInputBody.settings';

export const codec_SignInInputBody: Codec = {"kind": "object", "fields": {"cloud": {"ts": "cloud"}, "connection_id": {"ts": "connectionId"}, "name": {"ts": "name"}, "settings": {"ts": "settings", "codec": () => codec_SignInInputBody_settings}}, "required": ["cloud", "name"]};

import type { Codec } from '../codec-runtime';
import { codec_CreateInputBody_settings } from './CreateInputBody.settings';

export const codec_CreateInputBody: Codec = {"kind": "object", "fields": {"cloud": {"ts": "cloud"}, "credential": {"ts": "credential"}, "name": {"ts": "name"}, "settings": {"ts": "settings", "codec": () => codec_CreateInputBody_settings}}, "required": ["cloud", "credential", "name"]};

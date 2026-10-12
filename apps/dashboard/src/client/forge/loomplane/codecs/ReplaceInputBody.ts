import type { Codec } from '../codec-runtime';
import { codec_ReplaceInputBody_settings } from './ReplaceInputBody.settings';

export const codec_ReplaceInputBody: Codec = {"kind": "object", "fields": {"credential": {"ts": "credential"}, "settings": {"ts": "settings", "codec": () => codec_ReplaceInputBody_settings}}, "required": ["credential"]};

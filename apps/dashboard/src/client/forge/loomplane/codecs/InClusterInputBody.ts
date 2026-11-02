import type { Codec } from '../codec-runtime';
import { codec_InClusterInputBody_settings } from './InClusterInputBody.settings';

export const codec_InClusterInputBody: Codec = {"kind": "object", "fields": {"name": {"ts": "name"}, "settings": {"ts": "settings", "codec": () => codec_InClusterInputBody_settings}}, "required": ["name", "settings"]};

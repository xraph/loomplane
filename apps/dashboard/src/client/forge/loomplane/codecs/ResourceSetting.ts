import type { Codec } from '../codec-runtime';
import { codec_ResourceSetting_options } from './ResourceSetting.options';

export const codec_ResourceSetting: Codec = {"kind": "object", "fields": {"capacity": {"ts": "capacity"}, "default": {"ts": "default"}, "name": {"ts": "name"}, "options": {"ts": "options", "codec": () => codec_ResourceSetting_options}, "pattern": {"ts": "pattern"}, "required": {"ts": "required"}, "title": {"ts": "title"}}, "required": ["name", "pattern", "title"]};

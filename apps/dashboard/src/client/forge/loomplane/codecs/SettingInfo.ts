import type { Codec } from '../codec-runtime';

export const codec_SettingInfo: Codec = {"kind": "object", "fields": {"default": {"ts": "default"}, "name": {"ts": "name"}, "pattern": {"ts": "pattern"}, "required": {"ts": "required"}, "title": {"ts": "title"}}, "required": ["name", "pattern", "required", "title"]};

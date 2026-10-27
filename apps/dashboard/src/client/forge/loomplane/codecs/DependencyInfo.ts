import type { Codec } from '../codec-runtime';
import { codec_DependencyInfo_kinds } from './DependencyInfo.kinds';

export const codec_DependencyInfo: Codec = {"kind": "object", "fields": {"default": {"ts": "default"}, "external": {"ts": "external"}, "kinds": {"ts": "kinds", "codec": () => codec_DependencyInfo_kinds}, "name": {"ts": "name"}, "optional": {"ts": "optional"}, "title": {"ts": "title"}}, "required": ["default", "external", "kinds", "name", "optional", "title"]};

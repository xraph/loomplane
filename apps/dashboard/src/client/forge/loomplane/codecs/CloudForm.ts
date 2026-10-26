import type { Codec } from '../codec-runtime';
import { codec_CloudForm_methods } from './CloudForm.methods';
import { codec_CloudForm_settings } from './CloudForm.settings';

export const codec_CloudForm: Codec = {"kind": "object", "fields": {"builtin": {"ts": "builtin"}, "cloud": {"ts": "cloud"}, "credential": {"ts": "credential"}, "help": {"ts": "help"}, "methods": {"ts": "methods", "codec": () => codec_CloudForm_methods}, "settings": {"ts": "settings", "codec": () => codec_CloudForm_settings}, "title": {"ts": "title"}}, "required": ["builtin", "cloud", "methods", "title"]};

import type { Codec } from '../codec-runtime';
import { codec_CloudForm_in_cluster_settings } from './CloudForm.in_cluster_settings';
import { codec_CloudForm_keyless_settings } from './CloudForm.keyless_settings';
import { codec_CloudForm_methods } from './CloudForm.methods';
import { codec_CloudForm_settings } from './CloudForm.settings';
import { codec_CloudForm_sign_in_settings } from './CloudForm.sign_in_settings';

export const codec_CloudForm: Codec = {"kind": "object", "fields": {"builtin": {"ts": "builtin"}, "cloud": {"ts": "cloud"}, "credential": {"ts": "credential"}, "help": {"ts": "help"}, "in_cluster_settings": {"ts": "inClusterSettings", "codec": () => codec_CloudForm_in_cluster_settings}, "keyless_settings": {"ts": "keylessSettings", "codec": () => codec_CloudForm_keyless_settings}, "methods": {"ts": "methods", "codec": () => codec_CloudForm_methods}, "settings": {"ts": "settings", "codec": () => codec_CloudForm_settings}, "sign_in_settings": {"ts": "signInSettings", "codec": () => codec_CloudForm_sign_in_settings}, "title": {"ts": "title"}}, "required": ["builtin", "cloud", "methods", "title"]};

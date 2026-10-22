import type { Codec } from '../codec-runtime';
import { codec_EnvLink_options } from './EnvLink.options';

export const codec_EnvLink: Codec = {"kind": "object", "fields": {"address": {"ts": "address"}, "field": {"ts": "field"}, "from": {"ts": "from"}, "key": {"ts": "key"}, "options": {"ts": "options", "codec": () => codec_EnvLink_options}, "subject": {"ts": "subject"}}, "required": ["from", "key", "subject"]};

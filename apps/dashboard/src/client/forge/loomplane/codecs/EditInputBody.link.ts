import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_link_options } from './EditInputBody.link.options';

export const codec_EditInputBody_link: Codec = {"kind": "object", "fields": {"need": {"ts": "need"}, "options": {"ts": "options", "codec": () => codec_EditInputBody_link_options}, "service": {"ts": "service"}}, "required": ["need", "service"]};

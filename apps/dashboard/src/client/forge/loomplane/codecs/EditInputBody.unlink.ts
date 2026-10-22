import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_unlink_options } from './EditInputBody.unlink.options';

export const codec_EditInputBody_unlink: Codec = {"kind": "object", "fields": {"need": {"ts": "need"}, "options": {"ts": "options", "codec": () => codec_EditInputBody_unlink_options}, "service": {"ts": "service"}}, "required": ["need", "service"]};

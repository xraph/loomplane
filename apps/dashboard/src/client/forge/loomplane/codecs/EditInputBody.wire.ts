import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_wire_env } from './EditInputBody.wire.env';

export const codec_EditInputBody_wire: Codec = {"kind": "object", "fields": {"env": {"ts": "env", "codec": () => codec_EditInputBody_wire_env}, "need": {"ts": "need"}, "service": {"ts": "service"}}, "required": ["need", "service"]};

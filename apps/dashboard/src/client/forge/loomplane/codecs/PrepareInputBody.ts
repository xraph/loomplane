import type { Codec } from '../codec-runtime';
import { codec_PrepareInputBody_scope } from './PrepareInputBody.scope';

export const codec_PrepareInputBody: Codec = {"kind": "object", "fields": {"from": {"ts": "from"}, "replace": {"ts": "replace"}, "scope": {"ts": "scope", "codec": () => codec_PrepareInputBody_scope}, "to": {"ts": "to"}}};

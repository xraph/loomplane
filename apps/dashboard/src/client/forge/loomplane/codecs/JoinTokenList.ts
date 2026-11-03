import type { Codec } from '../codec-runtime';
import { codec_JoinTokenList_tokens } from './JoinTokenList.tokens';

export const codec_JoinTokenList: Codec = {"kind": "object", "fields": {"tokens": {"ts": "tokens", "codec": () => codec_JoinTokenList_tokens}}, "required": ["tokens"]};

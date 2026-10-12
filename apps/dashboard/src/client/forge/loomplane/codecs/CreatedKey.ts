import type { Codec } from '../codec-runtime';
import { codec_APIKey } from './APIKey';

export const codec_CreatedKey: Codec = {"kind": "object", "fields": {"key": {"ts": "key", "codec": () => codec_APIKey}, "secret": {"ts": "secret"}}, "required": ["key", "secret"]};

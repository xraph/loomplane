import type { Codec } from '../codec-runtime';
import { codec_Ref } from './Ref';

export const codec_Output: Codec = {"kind": "object", "fields": {"env_key": {"ts": "envKey"}, "from": {"ts": "from", "codec": () => codec_Ref}, "name": {"ts": "name"}, "per_consumer": {"ts": "perConsumer"}, "requires": {"ts": "requires"}, "secret": {"ts": "secret"}, "template": {"ts": "template"}}, "required": ["env_key", "name"]};

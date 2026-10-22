import type { Codec } from '../codec-runtime';
import { codec_Ref } from './Ref';

export const codec_EnvVar: Codec = {"kind": "object", "fields": {"from": {"ts": "from", "codec": () => codec_Ref}, "name": {"ts": "name"}, "requires": {"ts": "requires"}, "secret": {"ts": "secret"}, "template": {"ts": "template"}, "value": {"ts": "value"}}, "required": ["name"]};

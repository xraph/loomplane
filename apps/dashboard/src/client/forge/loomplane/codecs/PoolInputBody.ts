import type { Codec } from '../codec-runtime';
import { codec_PoolInputBody_members } from './PoolInputBody.members';

export const codec_PoolInputBody: Codec = {"kind": "object", "fields": {"accelerator_kind": {"ts": "acceleratorKind"}, "members": {"ts": "members", "codec": () => codec_PoolInputBody_members}, "name": {"ts": "name"}, "revision": {"ts": "revision"}}, "required": ["members", "name", "revision"]};

import type { Codec } from '../codec-runtime';
import { codec_Analysis } from './Analysis';
import { codec_Source } from './Source';

export const codec_SourceState: Codec = {"kind": "object", "fields": {"latest": {"ts": "latest", "codec": () => codec_Analysis}, "shown": {"ts": "shown", "codec": () => codec_Analysis}, "source": {"ts": "source", "codec": () => codec_Source}}, "required": ["source"]};

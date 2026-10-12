import type { Codec } from '../codec-runtime';
import { codec_Ignore_resources } from './Ignore.resources';

export const codec_Ignore: Codec = {"kind": "object", "fields": {"resources": {"ts": "resources", "codec": () => codec_Ignore_resources}}};

import type { Codec } from '../codec-runtime';
import { codec_Assignment } from './Assignment';

export const codec_EnvironmentCloud: Codec = {"kind": "object", "fields": {"assignment": {"ts": "assignment", "codec": () => codec_Assignment}, "revision": {"ts": "revision"}}, "required": ["assignment", "revision"]};

import type { Codec } from '../codec-runtime';
import { codec_EnvVar } from './EnvVar';

export const codec_ResolvedComponent_env: Codec = {"kind": "array", "items": () => codec_EnvVar};

import type { Codec } from '../codec-runtime';
import { codec_EnvUse } from './EnvUse';

export const codec_Service_env: Codec = {"kind": "array", "items": () => codec_EnvUse};

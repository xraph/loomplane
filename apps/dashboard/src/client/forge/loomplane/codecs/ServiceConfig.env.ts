import type { Codec } from '../codec-runtime';
import { codec_EnvLink } from './EnvLink';

export const codec_ServiceConfig_env: Codec = {"kind": "array", "items": () => codec_EnvLink};

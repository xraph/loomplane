import type { Codec } from '../codec-runtime';
import { codec_EnvironmentUsage } from './EnvironmentUsage';

export const codec_Usage_environments: Codec = {"kind": "array", "items": () => codec_EnvironmentUsage};

import type { Codec } from '../codec-runtime';
import { codec_Environment } from './Environment';

export const codec_EnvironmentList_environments: Codec = {"kind": "array", "items": () => codec_Environment};

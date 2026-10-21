import type { Codec } from '../codec-runtime';
import { codec_EnvironmentChange } from './EnvironmentChange';

export const codec_Sync_environments: Codec = {"kind": "array", "items": () => codec_EnvironmentChange};

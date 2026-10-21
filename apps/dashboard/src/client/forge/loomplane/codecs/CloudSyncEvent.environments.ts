import type { Codec } from '../codec-runtime';
import { codec_EnvironmentChange } from './EnvironmentChange';

export const codec_CloudSyncEvent_environments: Codec = {"kind": "array", "items": () => codec_EnvironmentChange};

import type { Codec } from '../codec-runtime';
import { codec_ServerWorkload } from './ServerWorkload';

export const codec_ServerUsage_workloads: Codec = {"kind": "array", "items": () => codec_ServerWorkload};

import type { Codec } from '../codec-runtime';
import { codec_NodeWorkload } from './NodeWorkload';

export const codec_NodeWorkloadList_workloads: Codec = {"kind": "array", "items": () => codec_NodeWorkload};

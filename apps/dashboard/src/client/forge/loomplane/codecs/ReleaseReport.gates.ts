import type { Codec } from '../codec-runtime';
import { codec_GateCheck } from './GateCheck';

export const codec_ReleaseReport_gates: Codec = {"kind": "array", "items": () => codec_GateCheck};

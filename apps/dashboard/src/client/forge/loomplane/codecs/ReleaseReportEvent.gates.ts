import type { Codec } from '../codec-runtime';
import { codec_GateCheck } from './GateCheck';

export const codec_ReleaseReportEvent_gates: Codec = {"kind": "array", "items": () => codec_GateCheck};

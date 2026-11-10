import type { Codec } from '../codec-runtime';
import { codec_RunningExec } from './RunningExec';

export const codec_FenceRepair_running: Codec = {"kind": "array", "items": () => codec_RunningExec};

import type { Codec } from '../codec-runtime';
import { codec_RunnerView } from './RunnerView';

export const codec_PoolView_runners: Codec = {"kind": "array", "items": () => codec_RunnerView};

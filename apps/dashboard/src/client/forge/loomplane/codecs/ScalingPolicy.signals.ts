import type { Codec } from '../codec-runtime';
import { codec_ScalingSignalRef } from './ScalingSignalRef';

export const codec_ScalingPolicy_signals: Codec = {"kind": "array", "items": () => codec_ScalingSignalRef};

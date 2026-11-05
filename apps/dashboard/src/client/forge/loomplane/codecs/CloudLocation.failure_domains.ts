import type { Codec } from '../codec-runtime';
import { codec_FailureDomain } from './FailureDomain';

export const codec_CloudLocation_failure_domains: Codec = {"kind": "array", "items": () => codec_FailureDomain};

import type { Codec } from '../codec-runtime';
import { codec_Retirement } from './Retirement';

export const codec_RetirementPage_retirements: Codec = {"kind": "array", "items": () => codec_Retirement};

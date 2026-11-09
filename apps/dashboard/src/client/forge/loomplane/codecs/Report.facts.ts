import type { Codec } from '../codec-runtime';
import { codec_Fact } from './Fact';

export const codec_Report_facts: Codec = {"kind": "array", "items": () => codec_Fact};

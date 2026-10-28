import type { Codec } from '../codec-runtime';
import { codec_QuantizationView } from './QuantizationView';

export const codec_EntryDetail_quantizations: Codec = {"kind": "array", "items": () => codec_QuantizationView};

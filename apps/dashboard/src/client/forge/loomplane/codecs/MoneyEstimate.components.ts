import type { Codec } from '../codec-runtime';
import { codec_MoneyComponent } from './MoneyComponent';

export const codec_MoneyEstimate_components: Codec = {"kind": "array", "items": () => codec_MoneyComponent};

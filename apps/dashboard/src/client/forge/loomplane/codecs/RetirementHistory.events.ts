import type { Codec } from '../codec-runtime';
import { codec_RetirementEvent } from './RetirementEvent';

export const codec_RetirementHistory_events: Codec = {"kind": "array", "items": () => codec_RetirementEvent};

import type { Codec } from '../codec-runtime';
import { codec_EventAcceptance } from './EventAcceptance';

export const codec_EventPage_acceptance: Codec = {"kind": "array", "items": () => codec_EventAcceptance};

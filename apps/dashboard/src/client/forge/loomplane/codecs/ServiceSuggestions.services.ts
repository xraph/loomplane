import type { Codec } from '../codec-runtime';
import { codec_ServiceSuggestion } from './ServiceSuggestion';

export const codec_ServiceSuggestions_services: Codec = {"kind": "array", "items": () => codec_ServiceSuggestion};

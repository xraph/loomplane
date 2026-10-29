import type { Codec } from '../codec-runtime';
import { codec_SoakService } from './SoakService';

export const codec_Soak_services: Codec = {"kind": "array", "items": () => codec_SoakService};

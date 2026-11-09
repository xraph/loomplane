import type { Codec } from '../codec-runtime';
import { codec_ChangedService } from './ChangedService';

export const codec_ReadinessEvidence_changed_services: Codec = {"kind": "array", "items": () => codec_ChangedService};

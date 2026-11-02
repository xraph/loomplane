import type { Codec } from '../codec-runtime';
import { codec_ServiceCoverage } from './ServiceCoverage';

export const codec_Coverage_services: Codec = {"kind": "array", "items": () => codec_ServiceCoverage};

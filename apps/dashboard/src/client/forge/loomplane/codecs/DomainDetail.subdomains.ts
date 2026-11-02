import type { Codec } from '../codec-runtime';
import { codec_ZoneOverview } from './ZoneOverview';

export const codec_DomainDetail_subdomains: Codec = {"kind": "array", "items": () => codec_ZoneOverview};

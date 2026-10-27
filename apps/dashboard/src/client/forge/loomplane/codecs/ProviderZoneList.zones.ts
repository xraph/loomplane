import type { Codec } from '../codec-runtime';
import { codec_DNSZone } from './DNSZone';

export const codec_ProviderZoneList_zones: Codec = {"kind": "array", "items": () => codec_DNSZone};

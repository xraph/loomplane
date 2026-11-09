import type { Codec } from '../codec-runtime';
import { codec_DNSRecord } from './DNSRecord';

export const codec_RouteOperation_desired_records: Codec = {"kind": "array", "items": () => codec_DNSRecord};

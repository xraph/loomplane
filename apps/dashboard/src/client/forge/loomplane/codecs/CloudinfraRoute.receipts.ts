import type { Codec } from '../codec-runtime';
import { codec_DNSRecord } from './DNSRecord';

export const codec_CloudinfraRoute_receipts: Codec = {"kind": "array", "items": () => codec_DNSRecord};

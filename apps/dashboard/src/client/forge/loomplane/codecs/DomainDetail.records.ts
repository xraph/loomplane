import type { Codec } from '../codec-runtime';
import { codec_ManagedRecord } from './ManagedRecord';

export const codec_DomainDetail_records: Codec = {"kind": "array", "items": () => codec_ManagedRecord};

import type { Codec } from '../codec-runtime';
import { codec_Package_evidence } from './Package.evidence';

export const codec_Package: Codec = {"kind": "object", "fields": {"ecosystem": {"ts": "ecosystem"}, "evidence": {"ts": "evidence", "codec": () => codec_Package_evidence}, "id": {"ts": "id"}, "name": {"ts": "name"}, "path": {"ts": "path"}, "runtime": {"ts": "runtime"}}, "required": ["ecosystem", "evidence", "id", "name", "path"]};

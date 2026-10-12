import type { Codec } from '../codec-runtime';
import { codec_Package_evidence } from './Package.evidence';
import { codec_Package_requires } from './Package.requires';

export const codec_Package: Codec = {"kind": "object", "fields": {"ecosystem": {"ts": "ecosystem"}, "evidence": {"ts": "evidence", "codec": () => codec_Package_evidence}, "id": {"ts": "id"}, "name": {"ts": "name"}, "path": {"ts": "path"}, "requires": {"ts": "requires", "codec": () => codec_Package_requires}, "runtime": {"ts": "runtime"}, "source": {"ts": "source"}}, "required": ["ecosystem", "evidence", "id", "name", "path"]};

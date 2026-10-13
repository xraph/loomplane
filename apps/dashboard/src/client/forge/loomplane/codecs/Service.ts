import type { Codec } from '../codec-runtime';
import { codec_Commands } from './Commands';
import { codec_Service_evidence } from './Service.evidence';
import { codec_Service_ports } from './Service.ports';

export const codec_Service: Codec = {"kind": "object", "fields": {"commands": {"ts": "commands", "codec": () => codec_Commands}, "confidence": {"ts": "confidence"}, "context": {"ts": "context"}, "deployable": {"ts": "deployable"}, "ecosystem": {"ts": "ecosystem"}, "entrypoint": {"ts": "entrypoint"}, "evidence": {"ts": "evidence", "codec": () => codec_Service_evidence}, "framework": {"ts": "framework"}, "id": {"ts": "id"}, "image": {"ts": "image"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "path": {"ts": "path"}, "ports": {"ts": "ports", "codec": () => codec_Service_ports}, "runtime": {"ts": "runtime"}, "unit": {"ts": "unit"}}, "required": ["commands", "confidence", "deployable", "ecosystem", "entrypoint", "evidence", "id", "kind", "name", "path"]};

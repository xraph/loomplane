import type { Codec } from '../codec-runtime';
import { codec_Commands } from './Commands';
import { codec_Service_evidence } from './Service.evidence';
import { codec_Service_ports } from './Service.ports';
import { codec_Service_requires } from './Service.requires';

export const codec_Service: Codec = {"kind": "object", "fields": {"commands": {"ts": "commands", "codec": () => codec_Commands}, "confidence": {"ts": "confidence"}, "deployable": {"ts": "deployable"}, "ecosystem": {"ts": "ecosystem"}, "entrypoint": {"ts": "entrypoint"}, "evidence": {"ts": "evidence", "codec": () => codec_Service_evidence}, "framework": {"ts": "framework"}, "id": {"ts": "id"}, "image": {"ts": "image"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "path": {"ts": "path"}, "ports": {"ts": "ports", "codec": () => codec_Service_ports}, "requires": {"ts": "requires", "codec": () => codec_Service_requires}, "runtime": {"ts": "runtime"}, "source": {"ts": "source"}, "unit": {"ts": "unit"}, "unit_name": {"ts": "unitName"}}, "required": ["commands", "confidence", "deployable", "ecosystem", "entrypoint", "evidence", "id", "kind", "name", "path"]};

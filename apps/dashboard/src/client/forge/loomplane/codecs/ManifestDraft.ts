import type { Codec } from '../codec-runtime';
import { codec_Manifest } from './Manifest';
import { codec_ManifestDraft_sources } from './ManifestDraft.sources';

export const codec_ManifestDraft: Codec = {"kind": "object", "fields": {"manifest": {"ts": "manifest", "codec": () => codec_Manifest}, "project_id": {"ts": "projectId"}, "sources": {"ts": "sources", "codec": () => codec_ManifestDraft_sources}, "yaml": {"ts": "yaml"}}, "required": ["project_id", "sources", "yaml"]};

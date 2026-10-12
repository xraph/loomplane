import type { Codec } from '../codec-runtime';
import { codec_Graph } from './Graph';
import { codec_ProjectView_sources } from './ProjectView.sources';

export const codec_ProjectView: Codec = {"kind": "object", "fields": {"graph": {"ts": "graph", "codec": () => codec_Graph}, "project_id": {"ts": "projectId"}, "sources": {"ts": "sources", "codec": () => codec_ProjectView_sources}}, "required": ["project_id", "sources"]};

import type { Codec } from '../codec-runtime';
import { codec_Graph } from './Graph';
import { codec_ProjectView_excluded } from './ProjectView.excluded';
import { codec_ProjectView_sources } from './ProjectView.sources';

export const codec_ProjectView: Codec = {"kind": "object", "fields": {"excluded": {"ts": "excluded", "codec": () => codec_ProjectView_excluded}, "graph": {"ts": "graph", "codec": () => codec_Graph}, "project_id": {"ts": "projectId"}, "sources": {"ts": "sources", "codec": () => codec_ProjectView_sources}}, "required": ["excluded", "project_id", "sources"]};

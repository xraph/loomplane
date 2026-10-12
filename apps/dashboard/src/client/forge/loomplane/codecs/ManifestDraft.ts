import type { Codec } from '../codec-runtime';
import { codec_Manifest } from './Manifest';

export const codec_ManifestDraft: Codec = {"kind": "object", "fields": {"analysis_id": {"ts": "analysisId"}, "commit_sha": {"ts": "commitSha"}, "manifest": {"ts": "manifest", "codec": () => codec_Manifest}, "project_id": {"ts": "projectId"}, "yaml": {"ts": "yaml"}}, "required": ["analysis_id", "commit_sha", "project_id", "yaml"]};

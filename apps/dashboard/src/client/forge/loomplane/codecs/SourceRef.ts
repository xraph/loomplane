import type { Codec } from '../codec-runtime';

export const codec_SourceRef: Codec = {"kind": "object", "fields": {"alias": {"ts": "alias"}, "analysis_id": {"ts": "analysisId"}, "commit_sha": {"ts": "commitSha"}, "repository": {"ts": "repository"}, "root": {"ts": "root"}}, "required": ["alias", "analysis_id", "commit_sha", "repository"]};

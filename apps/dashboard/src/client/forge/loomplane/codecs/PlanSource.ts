import type { Codec } from '../codec-runtime';

export const codec_PlanSource: Codec = {"kind": "object", "fields": {"alias": {"ts": "alias"}, "analysis_id": {"ts": "analysisId"}, "commit_sha": {"ts": "commitSha"}, "in_scope": {"ts": "inScope"}, "repository": {"ts": "repository"}, "root": {"ts": "root"}, "source_id": {"ts": "sourceId"}}, "required": ["alias", "analysis_id", "commit_sha", "in_scope", "repository", "root", "source_id"]};

import type { Codec } from '../codec-runtime';

export const codec_PlanSource: Codec = {"kind": "object", "fields": {"alias": {"ts": "alias"}, "analysis_id": {"ts": "analysisId"}, "commit_sha": {"ts": "commitSha"}, "from_plan_id": {"ts": "fromPlanId"}, "in_scope": {"ts": "inScope"}, "ref": {"ts": "ref"}, "repository": {"ts": "repository"}, "root": {"ts": "root"}, "source_id": {"ts": "sourceId"}}, "required": ["alias", "analysis_id", "commit_sha", "in_scope", "repository", "root", "source_id"]};

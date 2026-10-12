import type { Codec } from '../codec-runtime';

export const codec_DeployedRepository: Codec = {"kind": "object", "fields": {"alias": {"ts": "alias"}, "commit_sha": {"ts": "commitSha"}, "deployed_at": {"ts": "deployedAt"}, "plan_id": {"ts": "planId"}, "repository": {"ts": "repository"}, "source_id": {"ts": "sourceId"}}, "required": ["alias", "commit_sha", "deployed_at", "plan_id", "repository", "source_id"]};

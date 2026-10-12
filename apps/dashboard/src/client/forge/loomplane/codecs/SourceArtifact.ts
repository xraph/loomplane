import type { Codec } from '../codec-runtime';

export const codec_SourceArtifact: Codec = {"kind": "object", "fields": {"alias": {"ts": "alias"}, "commit_sha": {"ts": "commitSha"}, "key": {"ts": "key"}, "repository": {"ts": "repository"}}, "required": ["alias", "commit_sha", "key", "repository"]};

import type { Codec } from '../codec-runtime';

export const codec_ReusedArtifact: Codec = {"kind": "object", "fields": {"architecture": {"ts": "architecture"}, "commit_sha": {"ts": "commitSha"}, "digest": {"ts": "digest"}, "from_deployment_id": {"ts": "fromDeploymentId"}, "from_environment": {"ts": "fromEnvironment"}, "reference": {"ts": "reference"}, "revision": {"ts": "revision"}}, "required": ["commit_sha", "from_deployment_id", "from_environment"]};

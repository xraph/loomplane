import type { Codec } from '../codec-runtime';
import { codec_BitbucketStatus_problems } from './BitbucketStatus.problems';
import { codec_BitbucketStatus_workspaces } from './BitbucketStatus.workspaces';

export const codec_BitbucketStatus: Codec = {"kind": "object", "fields": {"can_connect": {"ts": "canConnect"}, "configured": {"ts": "configured"}, "problems": {"ts": "problems", "codec": () => codec_BitbucketStatus_problems}, "workspaces": {"ts": "workspaces", "codec": () => codec_BitbucketStatus_workspaces}}, "required": ["can_connect", "configured", "problems", "workspaces"]};

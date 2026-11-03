import type { Codec } from '../codec-runtime';
import { codec_BitbucketWorkspaceView } from './BitbucketWorkspaceView';

export const codec_BitbucketStatus_workspaces: Codec = {"kind": "array", "items": () => codec_BitbucketWorkspaceView};

import type { OperationMeta } from '../ops';
import { codec_GitLabSignInStart } from '../codecs/GitLabSignInStart';
import { codec_StartGitLabSignInInputBody } from '../codecs/StartGitLabSignInInputBody';

export const op_gitlab_startSignIn = {
  method: 'POST',
  path: '/api/v1/gitlab/sign-ins',
  provides: [],
  invalidates: [],
  bodyCodec: codec_StartGitLabSignInInputBody,
  responseCodec: codec_GitLabSignInStart,
} as const satisfies OperationMeta;

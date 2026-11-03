import type { OperationMeta } from '../ops';
import { codec_GitLabWebhookSecret } from '../codecs/GitLabWebhookSecret';
import { codec_SetGitLabWebhookInputBody } from '../codecs/SetGitLabWebhookInputBody';

export const op_gitlab_webhookSecret = {
  method: 'POST',
  path: '/api/v1/gitlab/connections/{id}/webhook',
  provides: [],
  invalidates: [],
  bodyCodec: codec_SetGitLabWebhookInputBody,
  responseCodec: codec_GitLabWebhookSecret,
} as const satisfies OperationMeta;

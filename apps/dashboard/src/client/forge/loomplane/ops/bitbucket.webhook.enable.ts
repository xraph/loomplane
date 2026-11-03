import type { OperationMeta } from '../ops';
import { codec_EnableBitbucketWebhookInputBody } from '../codecs/EnableBitbucketWebhookInputBody';
import { codec_PushState } from '../codecs/PushState';

export const op_bitbucket_webhook_enable = {
  method: 'POST',
  path: '/api/v1/bitbucket/webhooks',
  provides: [],
  invalidates: [],
  bodyCodec: codec_EnableBitbucketWebhookInputBody,
  responseCodec: codec_PushState,
} as const satisfies OperationMeta;

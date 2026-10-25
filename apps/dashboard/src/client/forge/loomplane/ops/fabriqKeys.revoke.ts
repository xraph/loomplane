import type { OperationMeta } from '../ops';
import { codec_ConsumerKey } from '../codecs/ConsumerKey';

export const op_fabriqKeys_revoke = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}/keys/{service}/revoke',
  entity: 'ConsumerKey',
  rootType: 'ConsumerKey',
  provides: ['ConsumerKey:{service}'],
  invalidates: ['ConsumerKey[]'],
  responseCodec: codec_ConsumerKey,
} as const satisfies OperationMeta;

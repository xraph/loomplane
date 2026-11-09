import type { OperationMeta } from '../ops';
import { codec_GitLabStatus } from '../codecs/GitLabStatus';

export const op_gitlab_status = {
  method: 'GET',
  path: '/api/v1/gitlab',
  rootType: 'GitLabStatus',
  provides: [],
  invalidates: [],
  responseCodec: codec_GitLabStatus,
} as const satisfies OperationMeta;

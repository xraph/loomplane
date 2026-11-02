import type { OperationMeta } from '../ops';
import { codec_Usage } from '../codecs/Usage';

export const op_organizationClouds_usage = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/usage',
  provides: [],
  invalidates: [],
  responseCodec: codec_Usage,
} as const satisfies OperationMeta;

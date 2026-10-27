import type { OperationMeta } from '../ops';
import { codec_Status } from '../codecs/Status';

export const op_fabriq_status = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}',
  provides: [],
  invalidates: [],
  responseCodec: codec_Status,
} as const satisfies OperationMeta;

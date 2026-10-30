import type { OperationMeta } from '../ops';
import { codec_FabriqloomStatus } from '../codecs/FabriqloomStatus';

export const op_fabriq_status = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}',
  provides: [],
  invalidates: [],
  responseCodec: codec_FabriqloomStatus,
} as const satisfies OperationMeta;

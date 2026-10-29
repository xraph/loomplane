import type { OperationMeta } from '../ops';
import { codec_Coverage } from '../codecs/Coverage';

export const op_releaseWatches_coverage = {
  method: 'GET',
  path: '/api/v1/projects/{id}/environments/{environment}/watch-coverage',
  provides: [],
  invalidates: [],
  responseCodec: codec_Coverage,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_ReleaseEvidence } from '../codecs/ReleaseEvidence';

export const op_releaseWatches_evidence = {
  method: 'GET',
  path: '/api/v1/projects/{id}/environments/{environment}/release-evidence',
  provides: [],
  invalidates: [],
  responseCodec: codec_ReleaseEvidence,
} as const satisfies OperationMeta;

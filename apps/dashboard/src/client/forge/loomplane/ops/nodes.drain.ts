import type { OperationMeta } from '../ops';
import { codec_DrainReview } from '../codecs/DrainReview';

export const op_nodes_drain = {
  method: 'POST',
  path: '/api/v1/nodes/{id}/drain',
  provides: [],
  invalidates: [],
  responseCodec: codec_DrainReview,
} as const satisfies OperationMeta;

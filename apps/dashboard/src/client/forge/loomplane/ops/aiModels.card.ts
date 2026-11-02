import type { OperationMeta } from '../ops';
import { codec_Card } from '../codecs/Card';

export const op_aiModels_card = {
  method: 'GET',
  path: '/api/v1/ai/models/registry/card',
  provides: [],
  invalidates: [],
  responseCodec: codec_Card,
} as const satisfies OperationMeta;

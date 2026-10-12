import type { OperationMeta } from '../ops';
import { codec_Analysis } from '../codecs/Analysis';

export const op_analyses_get = {
  method: 'GET',
  path: '/api/v1/analyses/{id}',
  entity: 'Analysis',
  rootType: 'Analysis',
  provides: ['Analysis:{id}'],
  invalidates: [],
  responseCodec: codec_Analysis,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_DefaultCloud } from '../codecs/DefaultCloud';

export const op_defaultCloud_get = {
  method: 'GET',
  path: '/api/v1/projects/{id}/default-cloud',
  entity: 'DefaultCloud',
  rootType: 'DefaultCloud',
  provides: ['DefaultCloud:{projectId}'],
  invalidates: [],
  responseCodec: codec_DefaultCloud,
} as const satisfies OperationMeta;

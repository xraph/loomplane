import type { OperationMeta } from '../ops';
import { codec_Resolution } from '../codecs/Resolution';

export const op_variables_resolve = {
  method: 'GET',
  path: '/api/v1/projects/{id}/variables/resolved',
  rootType: 'Resolution',
  provides: [],
  invalidates: [],
  responseCodec: codec_Resolution,
} as const satisfies OperationMeta;

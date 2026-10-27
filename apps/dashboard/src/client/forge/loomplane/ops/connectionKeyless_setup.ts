import type { OperationMeta } from '../ops';
import { codec_KeylessSetup } from '../codecs/KeylessSetup';

export const op_connectionKeyless_setup = {
  method: 'GET',
  path: '/api/v1/connections/{id}/keyless',
  provides: [],
  invalidates: [],
  responseCodec: codec_KeylessSetup,
} as const satisfies OperationMeta;

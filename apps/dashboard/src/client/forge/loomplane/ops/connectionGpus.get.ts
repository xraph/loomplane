import type { OperationMeta } from '../ops';
import { codec_GPUInventory } from '../codecs/GPUInventory';

export const op_connectionGpus_get = {
  method: 'GET',
  path: '/api/v1/connections/{id}/gpus',
  provides: [],
  invalidates: [],
  responseCodec: codec_GPUInventory,
} as const satisfies OperationMeta;

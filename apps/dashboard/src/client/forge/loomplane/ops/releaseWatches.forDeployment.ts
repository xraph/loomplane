import type { OperationMeta } from '../ops';
import { codec_Watch } from '../codecs/Watch';

export const op_releaseWatches_forDeployment = {
  method: 'GET',
  path: '/api/v1/deployments/{id}/watch',
  entity: 'Watch',
  rootType: 'Watch',
  provides: ['Watch:{id}'],
  invalidates: [],
  responseCodec: codec_Watch,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_InfrastructureStatus } from '../codecs/InfrastructureStatus';

export const op_infrastructure_get = {
  method: 'GET',
  path: '/api/v1/infrastructure',
  provides: [],
  invalidates: [],
  responseCodec: codec_InfrastructureStatus,
} as const satisfies OperationMeta;

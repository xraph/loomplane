import type { OperationMeta } from '../ops';
import { codec_EnvironmentCloud } from '../codecs/EnvironmentCloud';

export const op_environmentClouds_get = {
  method: 'GET',
  path: '/api/v1/projects/{id}/environments/{environmentId}/cloud',
  provides: [],
  invalidates: [],
  responseCodec: codec_EnvironmentCloud,
} as const satisfies OperationMeta;

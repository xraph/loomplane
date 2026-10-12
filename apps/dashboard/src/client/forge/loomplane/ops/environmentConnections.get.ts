import type { OperationMeta } from '../ops';
import { codec_EnvironmentConnections } from '../codecs/EnvironmentConnections';

export const op_environmentConnections_get = {
  method: 'GET',
  path: '/api/v1/projects/{id}/environments/{environment}/connections',
  provides: [],
  invalidates: [],
  responseCodec: codec_EnvironmentConnections,
} as const satisfies OperationMeta;

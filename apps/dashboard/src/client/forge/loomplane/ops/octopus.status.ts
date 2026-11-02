import type { OperationMeta } from '../ops';
import { codec_OctopusStatus } from '../codecs/OctopusStatus';

export const op_octopus_status = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/octopus/{resource}/status',
  provides: [],
  invalidates: [],
  responseCodec: codec_OctopusStatus,
} as const satisfies OperationMeta;

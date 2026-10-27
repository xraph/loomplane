import type { OperationMeta } from '../ops';
import { codec_DiscoverySummary } from '../codecs/DiscoverySummary';

export const op_octopus_discovery = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/octopus/{resource}/discovery',
  provides: [],
  invalidates: [],
  responseCodec: codec_DiscoverySummary,
} as const satisfies OperationMeta;

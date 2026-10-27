import type { OperationMeta } from '../ops';
import { codec_SyncResult } from '../codecs/SyncResult';

export const op_octopus_sync = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/octopus/{resource}/sync',
  provides: [],
  invalidates: [],
  responseCodec: codec_SyncResult,
} as const satisfies OperationMeta;

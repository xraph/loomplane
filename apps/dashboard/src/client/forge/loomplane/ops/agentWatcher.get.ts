import type { OperationMeta } from '../ops';
import { codec_AgentwatchStatus } from '../codecs/AgentwatchStatus';

export const op_agentWatcher_get = {
  method: 'GET',
  path: '/api/v1/agent/watcher',
  provides: [],
  invalidates: [],
  responseCodec: codec_AgentwatchStatus,
} as const satisfies OperationMeta;

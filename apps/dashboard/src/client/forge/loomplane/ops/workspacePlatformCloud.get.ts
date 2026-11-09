import type { OperationMeta } from '../ops';
import { codec_WorkspaceView } from '../codecs/WorkspaceView';

export const op_workspacePlatformCloud_get = {
  method: 'GET',
  path: '/api/v1/platform-cloud',
  provides: [],
  invalidates: [],
  responseCodec: codec_WorkspaceView,
} as const satisfies OperationMeta;

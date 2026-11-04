import type { OperationMeta } from '../ops';
import { codec_MoveAll } from '../codecs/MoveAll';

export const op_platformCloudHosts_moveAll = {
  method: 'POST',
  path: '/api/v1/platform/cloud/hosts/{host}/move-all',
  rootType: 'MoveAll',
  provides: [],
  invalidates: [],
  responseCodec: codec_MoveAll,
} as const satisfies OperationMeta;

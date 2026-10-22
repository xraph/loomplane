import type { OperationMeta } from '../ops';
import { codec_ServerList } from '../codecs/ServerList';

export const op_cloudServers_list = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/servers',
  provides: [],
  invalidates: [],
  responseCodec: codec_ServerList,
} as const satisfies OperationMeta;

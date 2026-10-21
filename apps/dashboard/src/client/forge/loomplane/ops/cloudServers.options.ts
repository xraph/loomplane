import type { OperationMeta } from '../ops';
import { codec_ServerOptions } from '../codecs/ServerOptions';

export const op_cloudServers_options = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/server-options/{connectionId}',
  provides: [],
  invalidates: [],
  responseCodec: codec_ServerOptions,
} as const satisfies OperationMeta;

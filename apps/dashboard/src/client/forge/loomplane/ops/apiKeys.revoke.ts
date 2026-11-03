import type { OperationMeta } from '../ops';
import { codec_CompromisedRevoke } from '../codecs/CompromisedRevoke';

export const op_apiKeys_revoke = {
  method: 'DELETE',
  path: '/api/v1/api-keys/{id}',
  entity: 'APIKey',
  rootType: 'CompromisedRevoke',
  provides: [],
  invalidates: ['APIKey[]'],
  responseCodec: codec_CompromisedRevoke,
} as const satisfies OperationMeta;

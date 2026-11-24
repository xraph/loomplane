import type { OperationMeta } from '../ops';
import { codec_DropUserInputBody } from '../codecs/DropUserInputBody';
import { codec_OwnUserList } from '../codecs/OwnUserList';

export const op_catalogEndpoints_dropUser = {
  method: 'POST',
  path: '/api/v1/catalog/endpoints/{id}/users/{user}/drops',
  provides: [],
  invalidates: [],
  bodyCodec: codec_DropUserInputBody,
  responseCodec: codec_OwnUserList,
} as const satisfies OperationMeta;

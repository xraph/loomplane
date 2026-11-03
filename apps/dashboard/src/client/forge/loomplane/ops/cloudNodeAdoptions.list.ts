import type { OperationMeta } from '../ops';
import { codec_NodeAdoptionList } from '../codecs/NodeAdoptionList';

export const op_cloudNodeAdoptions_list = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/node-adoptions',
  provides: [],
  invalidates: [],
  responseCodec: codec_NodeAdoptionList,
} as const satisfies OperationMeta;

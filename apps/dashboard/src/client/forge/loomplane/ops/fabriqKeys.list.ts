import type { OperationMeta } from '../ops';
import { codec_FabriqKeyList } from '../codecs/FabriqKeyList';

export const op_fabriqKeys_list = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}/keys',
  entity: 'ConsumerKey',
  rootType: 'FabriqKeyList',
  provides: ['ConsumerKey:{service}', 'ConsumerKey[]'],
  invalidates: [],
  responseCodec: codec_FabriqKeyList,
} as const satisfies OperationMeta;

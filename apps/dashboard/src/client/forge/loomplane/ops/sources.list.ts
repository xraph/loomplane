import type { OperationMeta } from '../ops';
import { codec_SourceList } from '../codecs/SourceList';

export const op_sources_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/sources',
  entity: 'DiscoverySource',
  rootType: 'SourceList',
  provides: ['DiscoverySource:{id}', 'DiscoverySource[]'],
  invalidates: [],
  responseCodec: codec_SourceList,
} as const satisfies OperationMeta;

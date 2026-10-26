import type { OperationMeta } from '../ops';
import { codec_AddSourceInputBody } from '../codecs/AddSourceInputBody';
import { codec_DiscoverySource } from '../codecs/DiscoverySource';

export const op_sources_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/sources',
  entity: 'DiscoverySource',
  rootType: 'DiscoverySource',
  provides: ['DiscoverySource:{id}'],
  invalidates: ['DiscoverySource[]'],
  bodyCodec: codec_AddSourceInputBody,
  responseCodec: codec_DiscoverySource,
} as const satisfies OperationMeta;

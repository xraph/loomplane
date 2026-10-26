import type { OperationMeta } from '../ops';
import { codec_DiscoverySource } from '../codecs/DiscoverySource';
import { codec_UpdateSourceInputBody } from '../codecs/UpdateSourceInputBody';

export const op_sources_update = {
  method: 'PATCH',
  path: '/api/v1/projects/{id}/sources/{alias}',
  entity: 'DiscoverySource',
  rootType: 'DiscoverySource',
  provides: ['DiscoverySource:{id}'],
  invalidates: ['DiscoverySource[]'],
  bodyCodec: codec_UpdateSourceInputBody,
  responseCodec: codec_DiscoverySource,
} as const satisfies OperationMeta;

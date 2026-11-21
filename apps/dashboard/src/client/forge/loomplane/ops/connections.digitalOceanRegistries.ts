import type { OperationMeta } from '../ops';
import { codec_DigitalOceanRegistryNames } from '../codecs/DigitalOceanRegistryNames';

export const op_connections_digitalOceanRegistries = {
  method: 'GET',
  path: '/api/v1/connections/{id}/digitalocean-registries',
  provides: [],
  invalidates: [],
  responseCodec: codec_DigitalOceanRegistryNames,
} as const satisfies OperationMeta;

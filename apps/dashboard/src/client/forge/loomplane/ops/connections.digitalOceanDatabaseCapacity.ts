import type { OperationMeta } from '../ops';
import { codec_DatabaseCapacity } from '../codecs/DatabaseCapacity';

export const op_connections_digitalOceanDatabaseCapacity = {
  method: 'GET',
  path: '/api/v1/connections/{id}/digitalocean-database-capacity',
  provides: [],
  invalidates: [],
  responseCodec: codec_DatabaseCapacity,
} as const satisfies OperationMeta;

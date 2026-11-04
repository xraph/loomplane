import type { OperationMeta } from '../ops';
import { codec_Region } from '../codecs/Region';
import { codec_RegionUpdateBody } from '../codecs/RegionUpdateBody';

export const op_platformCloudRegions_update = {
  method: 'PATCH',
  path: '/api/v1/platform/cloud/regions/{region}',
  entity: 'Region',
  rootType: 'Region',
  provides: ['Region:{id}'],
  invalidates: ['Region[]'],
  bodyCodec: codec_RegionUpdateBody,
  responseCodec: codec_Region,
} as const satisfies OperationMeta;

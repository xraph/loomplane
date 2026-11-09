import type { OperationMeta } from '../ops';
import { codec_Region } from '../codecs/Region';
import { codec_RegionInputBody } from '../codecs/RegionInputBody';

export const op_platformCloudRegions_create = {
  method: 'POST',
  path: '/api/v1/platform/cloud/regions',
  entity: 'Region',
  rootType: 'Region',
  provides: ['Region:{id}'],
  invalidates: ['Region[]'],
  bodyCodec: codec_RegionInputBody,
  responseCodec: codec_Region,
} as const satisfies OperationMeta;

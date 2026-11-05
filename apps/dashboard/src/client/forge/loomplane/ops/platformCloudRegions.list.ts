import type { OperationMeta } from '../ops';
import { codec_RegionList } from '../codecs/RegionList';

export const op_platformCloudRegions_list = {
  method: 'GET',
  path: '/api/v1/platform/cloud/regions',
  entity: 'Region',
  rootType: 'RegionList',
  provides: ['Region:{id}', 'Region[]'],
  invalidates: [],
  responseCodec: codec_RegionList,
} as const satisfies OperationMeta;

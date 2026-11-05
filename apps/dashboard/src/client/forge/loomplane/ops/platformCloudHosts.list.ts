import type { OperationMeta } from '../ops';
import { codec_HostList } from '../codecs/HostList';

export const op_platformCloudHosts_list = {
  method: 'GET',
  path: '/api/v1/platform/cloud/hosts',
  entity: 'HostView',
  rootType: 'HostList',
  provides: ['HostView:{id}', 'HostView[]'],
  invalidates: [],
  responseCodec: codec_HostList,
} as const satisfies OperationMeta;

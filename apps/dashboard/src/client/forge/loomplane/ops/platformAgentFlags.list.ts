import type { OperationMeta } from '../ops';
import { codec_FlagList } from '../codecs/FlagList';

export const op_platformAgentFlags_list = {
  method: 'GET',
  path: '/api/v1/platform/agent/flags',
  entity: 'Flag',
  rootType: 'FlagList',
  provides: ['Flag:{id}', 'Flag[]'],
  invalidates: [],
  responseCodec: codec_FlagList,
} as const satisfies OperationMeta;

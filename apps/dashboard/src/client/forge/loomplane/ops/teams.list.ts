import type { OperationMeta } from '../ops';
import { codec_TeamList } from '../codecs/TeamList';

export const op_teams_list = {
  method: 'GET',
  path: '/api/v1/teams',
  entity: 'Team',
  rootType: 'TeamList',
  provides: ['Team:{id}', 'Team[]'],
  invalidates: [],
  responseCodec: codec_TeamList,
} as const satisfies OperationMeta;

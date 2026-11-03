import type { OperationMeta } from '../ops';
import { codec_Team } from '../codecs/Team';

export const op_teams_get = {
  method: 'GET',
  path: '/api/v1/teams/{id}',
  entity: 'Team',
  rootType: 'Team',
  provides: ['Team:{id}'],
  invalidates: [],
  responseCodec: codec_Team,
} as const satisfies OperationMeta;

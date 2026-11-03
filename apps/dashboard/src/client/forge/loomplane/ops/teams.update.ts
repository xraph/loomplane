import type { OperationMeta } from '../ops';
import { codec_Team } from '../codecs/Team';
import { codec_UpdateTeamInputBody } from '../codecs/UpdateTeamInputBody';

export const op_teams_update = {
  method: 'PATCH',
  path: '/api/v1/teams/{id}',
  entity: 'Team',
  rootType: 'Team',
  provides: ['Team:{id}'],
  invalidates: ['Team[]'],
  bodyCodec: codec_UpdateTeamInputBody,
  responseCodec: codec_Team,
} as const satisfies OperationMeta;

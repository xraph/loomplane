import type { OperationMeta } from '../ops';
import { codec_Team } from '../codecs/Team';

export const op_teams_addMember = {
  method: 'PUT',
  path: '/api/v1/teams/{id}/members/{user_id}',
  entity: 'Team',
  rootType: 'Team',
  provides: ['Team:{id}'],
  invalidates: ['Team[]'],
  responseCodec: codec_Team,
} as const satisfies OperationMeta;

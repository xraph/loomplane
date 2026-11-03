import type { OperationMeta } from '../ops';
import { codec_CreateTeamInputBody } from '../codecs/CreateTeamInputBody';
import { codec_Team } from '../codecs/Team';

export const op_teams_create = {
  method: 'POST',
  path: '/api/v1/teams',
  entity: 'Team',
  rootType: 'Team',
  provides: ['Team:{id}'],
  invalidates: ['Team[]'],
  bodyCodec: codec_CreateTeamInputBody,
  responseCodec: codec_Team,
} as const satisfies OperationMeta;

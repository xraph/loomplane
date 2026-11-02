import type { OperationMeta } from '../ops';
import { codec_SetGrantInputBody } from '../codecs/SetGrantInputBody';
import { codec_Team } from '../codecs/Team';

export const op_teams_setGrant = {
  method: 'PUT',
  path: '/api/v1/teams/{id}/grants',
  entity: 'Team',
  rootType: 'Team',
  provides: ['Team:{id}'],
  invalidates: ['Team[]'],
  bodyCodec: codec_SetGrantInputBody,
  responseCodec: codec_Team,
} as const satisfies OperationMeta;

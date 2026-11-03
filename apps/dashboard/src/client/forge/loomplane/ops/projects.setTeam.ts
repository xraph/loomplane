import type { OperationMeta } from '../ops';
import { codec_Project } from '../codecs/Project';
import { codec_SetTeamInputBody } from '../codecs/SetTeamInputBody';

export const op_projects_setTeam = {
  method: 'PUT',
  path: '/api/v1/projects/{id}/team',
  entity: 'Project',
  rootType: 'Project',
  provides: ['Project:{id}'],
  invalidates: ['Project[]'],
  bodyCodec: codec_SetTeamInputBody,
  responseCodec: codec_Project,
} as const satisfies OperationMeta;

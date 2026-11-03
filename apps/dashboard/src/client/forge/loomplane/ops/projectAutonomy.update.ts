import type { OperationMeta } from '../ops';
import { codec_AutonomyInputBody } from '../codecs/AutonomyInputBody';
import { codec_Project } from '../codecs/Project';

export const op_projectAutonomy_update = {
  method: 'PUT',
  path: '/api/v1/projects/{id}/autonomy',
  entity: 'Project',
  rootType: 'Project',
  provides: ['Project:{id}'],
  invalidates: ['Project[]'],
  bodyCodec: codec_AutonomyInputBody,
  responseCodec: codec_Project,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_CreateProjectInputBody } from '../codecs/CreateProjectInputBody';
import { codec_Project } from '../codecs/Project';

export const op_projects_create = {
  method: 'POST',
  path: '/api/v1/projects',
  entity: 'Project',
  rootType: 'Project',
  provides: ['Project:{id}'],
  invalidates: ['Project[]'],
  bodyCodec: codec_CreateProjectInputBody,
  responseCodec: codec_Project,
} as const satisfies OperationMeta;

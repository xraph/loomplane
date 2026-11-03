import type { OperationMeta } from '../ops';
import { codec_ProjectPermissions } from '../codecs/ProjectPermissions';

export const op_projects_permissions = {
  method: 'GET',
  path: '/api/v1/projects/{id}/permissions',
  provides: [],
  invalidates: [],
  responseCodec: codec_ProjectPermissions,
} as const satisfies OperationMeta;

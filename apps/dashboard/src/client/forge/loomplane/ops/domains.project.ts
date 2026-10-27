import type { OperationMeta } from '../ops';
import { codec_ProjectDomainList } from '../codecs/ProjectDomainList';

export const op_domains_project = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/domains',
  provides: [],
  invalidates: [],
  responseCodec: codec_ProjectDomainList,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_CreateEnvironmentInputBody } from '../codecs/CreateEnvironmentInputBody';
import { codec_Environment } from '../codecs/Environment';

export const op_environments_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/environments',
  entity: 'Environment',
  rootType: 'Environment',
  provides: ['Environment:{id}'],
  invalidates: ['Environment[]'],
  bodyCodec: codec_CreateEnvironmentInputBody,
  responseCodec: codec_Environment,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_ChooseConnectionInputBody } from '../codecs/ChooseConnectionInputBody';
import { codec_EnvironmentConnections } from '../codecs/EnvironmentConnections';

export const op_environmentConnections_choose = {
  method: 'PUT',
  path: '/api/v1/projects/{id}/environments/{environment}/connections/{cloud}',
  provides: [],
  invalidates: [],
  bodyCodec: codec_ChooseConnectionInputBody,
  responseCodec: codec_EnvironmentConnections,
} as const satisfies OperationMeta;

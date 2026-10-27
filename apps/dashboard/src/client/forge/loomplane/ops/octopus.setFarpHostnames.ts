import type { OperationMeta } from '../ops';
import { codec_FarpHostnamesInputBody } from '../codecs/FarpHostnamesInputBody';
import { codec_OctopusStatus } from '../codecs/OctopusStatus';

export const op_octopus_setFarpHostnames = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/octopus/{resource}/farp-hostnames',
  provides: [],
  invalidates: [],
  bodyCodec: codec_FarpHostnamesInputBody,
  responseCodec: codec_OctopusStatus,
} as const satisfies OperationMeta;

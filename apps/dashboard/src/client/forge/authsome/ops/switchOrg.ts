import type { OperationMeta } from '../ops';
import { codec_SwitchOrgRequest } from '../codecs/SwitchOrgRequest';
import { codec_SwitchOrgResponse } from '../codecs/SwitchOrgResponse';

export const op_switchOrg = {
  method: 'POST',
  path: '/authsome/v1/me/switch-org',
  provides: [],
  invalidates: [],
  security: ['session', 'session-cookie'],
  bodyCodec: codec_SwitchOrgRequest,
  responseCodec: codec_SwitchOrgResponse,
} as const satisfies OperationMeta;

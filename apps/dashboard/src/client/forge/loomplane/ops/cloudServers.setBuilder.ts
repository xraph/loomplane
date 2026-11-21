import type { OperationMeta } from '../ops';
import { codec_ServerBuilderSetting } from '../codecs/ServerBuilderSetting';
import { codec_ServerBuilderSettingBody } from '../codecs/ServerBuilderSettingBody';

export const op_cloudServers_setBuilder = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/servers/{serverId}/builder',
  provides: [],
  invalidates: [],
  bodyCodec: codec_ServerBuilderSettingBody,
  responseCodec: codec_ServerBuilderSetting,
} as const satisfies OperationMeta;

import type { OperationMeta } from '../ops';
import { codec_PushDevice } from '../codecs/PushDevice';
import { codec_RegisterDeviceInputBody } from '../codecs/RegisterDeviceInputBody';

export const op_push_devices_create = {
  method: 'POST',
  path: '/api/v1/push/devices',
  entity: 'PushDevice',
  rootType: 'PushDevice',
  provides: ['PushDevice:{id}'],
  invalidates: ['PushDevice[]'],
  bodyCodec: codec_RegisterDeviceInputBody,
  responseCodec: codec_PushDevice,
} as const satisfies OperationMeta;

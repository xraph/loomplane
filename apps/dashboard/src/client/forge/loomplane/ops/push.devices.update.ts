import type { OperationMeta } from '../ops';
import { codec_PushDevice } from '../codecs/PushDevice';
import { codec_UpdateDeviceInputBody } from '../codecs/UpdateDeviceInputBody';

export const op_push_devices_update = {
  method: 'PUT',
  path: '/api/v1/push/devices/{id}',
  entity: 'PushDevice',
  rootType: 'PushDevice',
  provides: ['PushDevice:{id}'],
  invalidates: ['PushDevice[]'],
  bodyCodec: codec_UpdateDeviceInputBody,
  responseCodec: codec_PushDevice,
} as const satisfies OperationMeta;

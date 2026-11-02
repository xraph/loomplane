import { mutation } from '@forge-go/client-core';
import { op_push_devices_update } from '../ops/push.devices.update';
import type { PushDevice } from '../types';

export const usePushDevicesUpdate = /*#__PURE__*/ mutation<PushDevice, PushDevice>(op_push_devices_update);

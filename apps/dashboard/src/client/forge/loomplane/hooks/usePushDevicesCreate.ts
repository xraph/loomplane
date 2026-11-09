import { mutation } from '@forge-go/client-core';
import { op_push_devices_create } from '../ops/push.devices.create';
import type { PushDevice } from '../types';

export const usePushDevicesCreate = /*#__PURE__*/ mutation<PushDevice, PushDevice>(op_push_devices_create);

import { mutation } from '@forge-go/client-core';
import { op_platformCloudHosts_setAllotment } from '../ops/platformCloudHosts.setAllotment';
import type { Host } from '../types';

export const usePlatformCloudHostsSetAllotment = /*#__PURE__*/ mutation<Host, Host>(op_platformCloudHosts_setAllotment);

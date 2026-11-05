import { mutation } from '@forge-go/client-core';
import { op_platformCloudHosts_check } from '../ops/platformCloudHosts.check';
import type { Host } from '../types';

export const usePlatformCloudHostsCheck = /*#__PURE__*/ mutation<Host, Host>(op_platformCloudHosts_check);

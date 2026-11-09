import { mutation } from '@forge-go/client-core';
import { op_platformCloudHosts_drain } from '../ops/platformCloudHosts.drain';
import type { Host } from '../types';

export const usePlatformCloudHostsDrain = /*#__PURE__*/ mutation<Host, Host>(op_platformCloudHosts_drain);

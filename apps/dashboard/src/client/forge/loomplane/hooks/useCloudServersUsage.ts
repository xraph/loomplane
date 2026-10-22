import { query } from '@forge-go/client-core';
import { op_cloudServers_usage } from '../ops/cloudServers.usage';
import type { ServerUsage } from '../types';

export const useCloudServersUsage = /*#__PURE__*/ query<ServerUsage>(op_cloudServers_usage);

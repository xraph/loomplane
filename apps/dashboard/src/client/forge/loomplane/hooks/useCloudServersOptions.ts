import { query } from '@forge-go/client-core';
import { op_cloudServers_options } from '../ops/cloudServers.options';
import type { ServerOptions } from '../types';

export const useCloudServersOptions = /*#__PURE__*/ query<ServerOptions>(op_cloudServers_options);

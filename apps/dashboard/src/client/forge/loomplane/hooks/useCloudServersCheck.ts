import { mutation } from '@forge-go/client-core';
import { op_cloudServers_check } from '../ops/cloudServers.check';
import type { Server } from '../types';

export const useCloudServersCheck = /*#__PURE__*/ mutation<Server, Server>(op_cloudServers_check);

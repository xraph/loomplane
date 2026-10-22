import { mutation } from '@forge-go/client-core';
import { op_cloudServers_applyDrain } from '../ops/cloudServers.applyDrain';
import type { Server } from '../types';

export const useCloudServersApplyDrain = /*#__PURE__*/ mutation<Server, Server>(op_cloudServers_applyDrain);

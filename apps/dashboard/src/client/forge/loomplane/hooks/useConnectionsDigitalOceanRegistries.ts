import { query } from '@forge-go/client-core';
import { op_connections_digitalOceanRegistries } from '../ops/connections.digitalOceanRegistries';
import type { DigitalOceanRegistryNames } from '../types';

export const useConnectionsDigitalOceanRegistries = /*#__PURE__*/ query<DigitalOceanRegistryNames>(op_connections_digitalOceanRegistries);

import { query } from '@forge-go/client-core';
import { op_publicConfig_get } from '../ops/publicConfig.get';
import type { PublicConfig } from '../types';

export const usePublicConfigGet = /*#__PURE__*/ query<PublicConfig>(op_publicConfig_get);

import { query } from '@forge-go/client-core';
import { op_push_config_get } from '../ops/push.config.get';
import type { PushConfig } from '../types';

export const usePushConfigGet = /*#__PURE__*/ query<PushConfig>(op_push_config_get);

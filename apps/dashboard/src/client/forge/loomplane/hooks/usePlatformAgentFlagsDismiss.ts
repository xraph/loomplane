import { mutation } from '@forge-go/client-core';
import { op_platformAgentFlags_dismiss } from '../ops/platformAgentFlags.dismiss';
import type { Flag } from '../types';

export const usePlatformAgentFlagsDismiss = /*#__PURE__*/ mutation<Flag, Flag>(op_platformAgentFlags_dismiss);

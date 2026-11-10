import { query } from '@forge-go/client-core';
import { op_platformAgentFlags_list } from '../ops/platformAgentFlags.list';
import type { FlagList } from '../types';

export const usePlatformAgentFlagsList = /*#__PURE__*/ query<FlagList>(op_platformAgentFlags_list);

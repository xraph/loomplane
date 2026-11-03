import { query } from '@forge-go/client-core';
import { op_platformCloudSignInApps_list } from '../ops/platformCloudSignInApps.list';
import type { SignInAppList } from '../types';

export const usePlatformCloudSignInAppsList = /*#__PURE__*/ query<SignInAppList>(op_platformCloudSignInApps_list);

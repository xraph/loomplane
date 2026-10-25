import { query } from '@forge-go/client-core';
import { op_cloudSignInApps_list } from '../ops/cloudSignInApps.list';
import type { SignInAppList } from '../types';

export const useCloudSignInAppsList = /*#__PURE__*/ query<SignInAppList>(op_cloudSignInApps_list);

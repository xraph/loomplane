import { mutation } from '@forge-go/client-core';
import { op_nativeRetentionPolicies_set } from '../ops/nativeRetentionPolicies.set';
import type { NativeRetentionPolicy } from '../types';

export const useNativeRetentionPoliciesSet = /*#__PURE__*/ mutation<NativeRetentionPolicy, NativeRetentionPolicy>(op_nativeRetentionPolicies_set);

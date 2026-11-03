import { query } from '@forge-go/client-core';
import { op_bitbucket_status } from '../ops/bitbucket.status';
import type { BitbucketStatus } from '../types';

export const useBitbucketStatus = /*#__PURE__*/ query<BitbucketStatus>(op_bitbucket_status);

import { query } from '@forge-go/client-core';
import { op_bitbucket_repositories } from '../ops/bitbucket.repositories';
import type { BitbucketRepositoryList } from '../types';

export const useBitbucketRepositories = /*#__PURE__*/ query<BitbucketRepositoryList>(op_bitbucket_repositories);

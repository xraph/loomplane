import { query } from '@forge-go/client-core';
import { op_gitlab_status } from '../ops/gitlab.status';
import type { GitLabStatus } from '../types';

export const useGitlabStatus = /*#__PURE__*/ query<GitLabStatus>(op_gitlab_status);

import { query } from '@forge-go/client-core';
import { op_github_status } from '../ops/github.status';
import type { GitHubStatus } from '../types';

export const useGithubStatus = /*#__PURE__*/ query<GitHubStatus>(op_github_status);

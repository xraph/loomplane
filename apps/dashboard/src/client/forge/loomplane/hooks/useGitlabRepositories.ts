import { query } from '@forge-go/client-core';
import { op_gitlab_repositories } from '../ops/gitlab.repositories';
import type { GitLabRepositoryList } from '../types';

export const useGitlabRepositories = /*#__PURE__*/ query<GitLabRepositoryList>(op_gitlab_repositories);

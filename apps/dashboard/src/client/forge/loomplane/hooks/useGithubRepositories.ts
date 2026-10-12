import { query } from '@forge-go/client-core';
import { op_github_repositories } from '../ops/github.repositories';
import type { ConnectedRepositoryList } from '../types';

export const useGithubRepositories = /*#__PURE__*/ query<ConnectedRepositoryList>(op_github_repositories);

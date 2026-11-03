import type { Codec } from '../codec-runtime';
import { codec_GitLabRepositoryList_repositories } from './GitLabRepositoryList.repositories';

export const codec_GitLabRepositoryList: Codec = {"kind": "object", "fields": {"repositories": {"ts": "repositories", "codec": () => codec_GitLabRepositoryList_repositories}}, "required": ["repositories"]};

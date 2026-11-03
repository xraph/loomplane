import type { Codec } from '../codec-runtime';
import { codec_GitLabRepository } from './GitLabRepository';

export const codec_GitLabRepositoryList_repositories: Codec = {"kind": "array", "items": () => codec_GitLabRepository};

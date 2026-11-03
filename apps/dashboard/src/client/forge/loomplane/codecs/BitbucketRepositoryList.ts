import type { Codec } from '../codec-runtime';
import { codec_BitbucketRepositoryList_problems } from './BitbucketRepositoryList.problems';
import { codec_BitbucketRepositoryList_repositories } from './BitbucketRepositoryList.repositories';

export const codec_BitbucketRepositoryList: Codec = {"kind": "object", "fields": {"problems": {"ts": "problems", "codec": () => codec_BitbucketRepositoryList_problems}, "repositories": {"ts": "repositories", "codec": () => codec_BitbucketRepositoryList_repositories}}, "required": ["problems", "repositories"]};

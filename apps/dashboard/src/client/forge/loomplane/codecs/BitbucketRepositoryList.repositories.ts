import type { Codec } from '../codec-runtime';
import { codec_ConnectedBitbucketRepository } from './ConnectedBitbucketRepository';

export const codec_BitbucketRepositoryList_repositories: Codec = {"kind": "array", "items": () => codec_ConnectedBitbucketRepository};

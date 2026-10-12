import type { Codec } from '../codec-runtime';
import { codec_ConnectedRepositoryList_repositories } from './ConnectedRepositoryList.repositories';

export const codec_ConnectedRepositoryList: Codec = {"kind": "object", "fields": {"repositories": {"ts": "repositories", "codec": () => codec_ConnectedRepositoryList_repositories}}, "required": ["repositories"]};

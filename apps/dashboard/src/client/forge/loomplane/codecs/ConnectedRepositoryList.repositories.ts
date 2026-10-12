import type { Codec } from '../codec-runtime';
import { codec_ConnectedRepository } from './ConnectedRepository';

export const codec_ConnectedRepositoryList_repositories: Codec = {"kind": "array", "items": () => codec_ConnectedRepository};

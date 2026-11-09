import type { Codec } from '../codec-runtime';
import { codec_ClusterProblem_categories } from './ClusterProblem.categories';

export const codec_ClusterProblem: Codec = {"kind": "object", "fields": {"categories": {"ts": "categories", "codec": () => codec_ClusterProblem_categories}, "code": {"ts": "code"}, "message": {"ts": "message"}, "resource": {"ts": "resource"}, "retryable": {"ts": "retryable"}}, "required": ["code", "message", "retryable"]};

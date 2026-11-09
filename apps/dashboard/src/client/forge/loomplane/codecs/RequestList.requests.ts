import type { Codec } from '../codec-runtime';
import { codec_Request } from './Request';

export const codec_RequestList_requests: Codec = {"kind": "array", "items": () => codec_Request};

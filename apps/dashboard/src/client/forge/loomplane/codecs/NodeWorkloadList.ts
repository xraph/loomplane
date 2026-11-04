import type { Codec } from '../codec-runtime';
import { codec_NodeWorkloadList_workloads } from './NodeWorkloadList.workloads';

export const codec_NodeWorkloadList: Codec = {"kind": "object", "fields": {"workloads": {"ts": "workloads", "codec": () => codec_NodeWorkloadList_workloads}}, "required": ["workloads"]};

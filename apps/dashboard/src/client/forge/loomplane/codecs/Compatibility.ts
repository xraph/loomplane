import type { Codec } from '../codec-runtime';
import { codec_Compatibility_control_versions } from './Compatibility.control_versions';
import { codec_Compatibility_read_data_versions } from './Compatibility.read_data_versions';

export const codec_Compatibility: Codec = {"kind": "object", "fields": {"control_versions": {"ts": "controlVersions", "codec": () => codec_Compatibility_control_versions}, "read_data_versions": {"ts": "readDataVersions", "codec": () => codec_Compatibility_read_data_versions}, "write_data_version": {"ts": "writeDataVersion"}}};

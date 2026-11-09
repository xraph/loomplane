import type { Codec } from '../codec-runtime';
import { codec_PlatformLimits } from './PlatformLimits';
import { codec_PlatformcloudUse } from './PlatformcloudUse';
import { codec_WorkspaceView_placements } from './WorkspaceView.placements';
import { codec_WorkspaceView_regions } from './WorkspaceView.regions';
import { codec_WorkspaceView_suspend_reasons } from './WorkspaceView.suspend_reasons';

export const codec_WorkspaceView: Codec = {"kind": "object", "fields": {"enabled": {"ts": "enabled"}, "limits": {"ts": "limits", "codec": () => codec_PlatformLimits}, "placements": {"ts": "placements", "codec": () => codec_WorkspaceView_placements}, "regions": {"ts": "regions", "codec": () => codec_WorkspaceView_regions}, "suspend_reasons": {"ts": "suspendReasons", "codec": () => codec_WorkspaceView_suspend_reasons}, "use": {"ts": "use", "codec": () => codec_PlatformcloudUse}}, "required": ["enabled", "limits", "placements", "regions", "suspend_reasons", "use"]};

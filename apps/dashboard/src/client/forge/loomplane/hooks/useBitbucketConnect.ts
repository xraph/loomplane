import { mutation } from '@forge-go/client-core';
import { op_bitbucket_connect } from '../ops/bitbucket.connect';
import type { BitbucketWorkspaceView } from '../types';

export const useBitbucketConnect = /*#__PURE__*/ mutation<BitbucketWorkspaceView, BitbucketWorkspaceView>(op_bitbucket_connect);

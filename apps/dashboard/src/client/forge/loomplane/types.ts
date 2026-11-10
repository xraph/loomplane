// Generated types

export enum ConnectionState {
  DISCONNECTED = 'disconnected',
  CONNECTING = 'connecting',
  CONNECTED = 'connected',
  RECONNECTING = 'reconnecting',
  CLOSED = 'closed',
  ERROR = 'error',
}

export interface APIKey {
  access: string;
  createdAt: string;
  createdBy: string;
  expiresAt?: string;
  hint: string;
  id: string;
  lastUsedAt?: string;
  name: string;
  revokedAt?: string;
  state: string;
}

export interface APIKeyList {
  keys: APIKey[];
}

export interface Accelerator {
  count: number;
  kind: string;
  memoryGib: number;
  vendor: string;
}

export interface AccountSpend {
  cloud: string;
  connectionId: string;
  currency: string;
  days: Point[];
  name: string;
  total: number;
}

export interface Action {
  adoptCloudPlacement?: boolean;
  backupEveryHours?: number;
  changes?: Change[];
  cleanupTargets?: CleanupTarget[];
  cloudAdoption?: CloudAdoption;
  compensation?: CloudCompensation;
  connectionId?: string;
  creates: string;
  dependsOn?: string[];
  engine: string;
  id: string;
  name: string;
  op: string;
  provider: string;
  reason: string;
  region?: string;
  resource?: ResourceConfig;
  routes?: CloudinfraRoute[];
  service?: ServiceConfig;
  step: number;
  subject: string;
}

export interface ActivityDay {
  date: string;
  failed: number;
  succeeded: number;
}

export interface ActivitySummary {
  analyses: AnalysisCounts;
  daily: ActivityDay[];
  days: number;
  deployments: DeploymentCounts;
  drift: DriftCounts;
  plans: PlanCounts;
  since: string;
}

export interface AddGitLabHostInputBody {
  clientId: string;
  clientSecret: string;
  webUrl: string;
}

export interface AddSourceInputBody {
  alias?: string;
  ref?: string;
  repository: string;
  root?: string;
}

export interface AgentBudgetInputBody {
  monthlyBudgetUsd: string;
  revision: number;
}

export interface AgentUpdateInputBody {
  enabled?: boolean;
  knowledge?: boolean;
  model?: string;
  providerKey?: string;
  revision: number;
}

export interface AgentView {
  available: boolean;
  budget: BudgetState;
  effective: EffectiveView;
  enabled: boolean;
  key?: KeyView;
  knowledge: boolean;
  knowledgeStatus: KnowledgeStatus;
  model: string;
  nexusTenantId: string;
  orgId: string;
  problem?: string;
  providerKey: string;
  revision: number;
  server: ServerDefaults;
  updatedAt: string;
  updatedBy?: string;
}

export interface AgentreportsEvidence {
  analysisId?: string;
  bindingId?: string;
  deploymentId?: string;
  planId?: string;
  sourceAlias?: string;
  verdictIds?: string[];
  watchId?: string;
}

export interface AgentrequestsTarget {
  cloud?: string;
  environment?: string;
  key?: string;
  planId?: string;
  planRevision?: string;
  repository?: string;
  service?: string;
}

export interface AgentsettingsStatus {
  enabled: boolean;
  knowledge: boolean;
  server: boolean;
}

export interface AgentwatchStatus {
  issues: IssuesStatus;
  paused: boolean;
  pausedAt?: string;
  pausedReason?: string;
  tickedAt?: string;
}

export interface Allocation {
  environmentId?: string;
  label: string;
  monthlyBudgetUsd: string;
  projectId?: string;
  scope: string;
  updatedAt: string;
}

export interface Allotment {
  diskMib: number;
  memoryMib: number;
  vcpus: number;
  vms: number;
}

export interface AllotmentBody {
  diskMib: number;
  memoryMib: number;
  vcpus: number;
  vms: number;
}

export interface Amount {
  currency: string;
  days: Point[];
  total: number;
  undated: number;
}

export interface Analysis {
  commitSha: string;
  createdAt: string;
  createdBy: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  pinned: boolean;
  projectId: string;
  ref: string;
  repository: string;
  source: string;
  sourceId: string;
  startedAt?: string;
  status: string;
  summary: GraphSummary;
}

export interface AnalysisCounts {
  failed: number;
  succeeded: number;
}

export interface AnalysisGraph {
  graph: Graph;
  id: string;
}

export interface AnalysisList {
  analyses: Analysis[];
}

export interface AnswerInputBody {
  text: string;
}

export interface AppInputBody {
  clientId: string;
  clientSecret: string;
  slug?: string;
}

export interface ApplyCloudInputBody {
  idempotencyKey: string;
  previewId: string;
  revision: string;
}

export interface ApplyInputBody {
  review: string;
}

export interface ApproveExpansionInputBody {
  expectedRevision: number;
}

export interface ArtifactChoice {
  from?: string;
  reason?: string;
  reference?: string;
  reused: boolean;
  revision?: string;
  service: string;
}

export interface Assignment {
  cloudId: string;
  cloudRevision: number;
  defaultPlacement?: DefaultPlacement;
  environmentId: string;
  grantRevision: number;
  orgId: string;
  projectId: string;
  revision: number;
  targets: CloudprofilesTarget[];
}

export interface AttachDefaults {
  blockedPathPrefixes: string[];
  cloudId?: string;
  environmentId: string;
  port: number;
  service: string;
}

export interface AttachmentInput {
  adoptNamespace: boolean;
  cloudRevision: number;
  endpointFingerprint: string;
  grantRevision: number;
  namespace: string;
  namespaceUid?: string;
  prerequisites: AttachmentPrerequisites;
  projectId: string;
  resourceRevision: number;
  runtimeAccess?: RuntimeAccessProfile;
  serviceAccountName: string;
  targetSlug: string;
  trustFingerprint: string;
}

export interface AttachmentPrerequisites {
  ingressAddress?: string;
  registryHostname?: string;
  requireBuilds: boolean;
  requireIngress: boolean;
  requirePersistentStorage: boolean;
  requireTls: boolean;
  storageClass?: string;
}

export interface AuthorizeBitbucketInputBody {
  workspace: string;
}

export interface AutoRenewInputBody {
  autoRenew: boolean;
  revision: number;
}

export interface AutonomyInputBody {
  autonomousPromotion?: boolean;
  minStagingSoakSeconds?: number;
}

export interface Backup {
  attempt: number;
  bytes: number;
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  environmentId: string;
  error?: string;
  id: string;
  name: string;
  orgId: string;
  projectId: string;
  restoredAt?: string;
  sha256?: string;
  status: string;
}

export interface BackupCreateInputBody {
  environment: string;
  requestId: string;
  resource: string;
}

export interface BackupList {
  backups: Backup[];
}

export interface Binding {
  alias: string;
  commitSha: string;
  config: string;
  connectionId: string;
  createdAt: string;
  drift: string;
  driftMessage: string;
  engine: string;
  environmentId: string;
  externalId: string;
  id: string;
  name: string;
  observedAt?: string;
  orgId: string;
  ownedResources?: Record<string, any>;
  physicalId?: string;
  planId: string;
  projectId: string;
  provider: string;
  region: string;
  revision: string;
  subject: string;
  suggestedAlias?: string;
  updatedAt: string;
  url: string;
}

export interface BindingAliasInputBody {
  alias: string;
}

export interface BindingList {
  bindings: Binding[];
}

export interface BitbucketAuthorization {
  url: string;
}

export interface BitbucketConnectInputBody {
  code: string;
  state: string;
}

export interface BitbucketRepositoryList {
  problems: string[];
  repositories: ConnectedBitbucketRepository[];
}

export interface BitbucketStatus {
  canConnect: boolean;
  configured: boolean;
  problems: string[];
  workspaces: BitbucketWorkspaceView[];
}

export interface BitbucketWorkspaceView {
  avatarUrl: string;
  connectedBy: string;
  createdAt: string;
  id: string;
  manageUrl: string;
  message: string;
  name: string;
  status: string;
  workspace: string;
}

export interface Board {
  counts: SourceCounts;
  discovery: DiscoverySummary;
  gateway: OctopusStatus;
  hosts: HostCard[];
  live: boolean;
  liveError?: string;
  unroutable: UnroutableService[];
  unrouted: ServicePort[];
}

export interface BoardRoute {
  key: string;
  methods: string[];
  note: string;
  overrides: boolean;
  path: string;
  port: number;
  revision?: number;
  routeId?: string;
  service: string;
  source: string;
  stripPrefix: boolean;
  sync?: string;
  syncError?: string;
}

export interface BudgetReview {
  cloudId: string;
  digest: string;
  estimate: MoneyEstimate;
  policyRevision: number;
}

export interface BudgetState {
  exhausted: boolean;
  limitUsd: string;
  spentUsd: string;
}

export interface Build {
  builder?: string;
  context?: string;
  image?: string;
  registry?: string;
}

export interface BuildSystem {
  evidence: DiscoveryEvidence;
  name: string;
  path: string;
  source?: string;
}

export interface BuildTarget {
  builder?: string;
  builderCloud?: string;
  builderSettings?: Record<string, any>;
  image?: string;
  registry?: string;
  registrySettings?: Record<string, any>;
}

export interface CLIOrganization {
  id: string;
  name: string;
  slug: string;
}

export interface CLIOrganizationList {
  active?: string;
  organizations: CLIOrganization[];
}

export interface Canary {
  holdSeconds: number;
  reason?: string;
  services: CanaryService[];
  status: string;
  step: number;
  steps: number[];
  thresholds: Thresholds;
  updatedAt: string;
  weight: number;
}

export interface CanaryReport {
  canary?: Canary;
  verdicts: CanaryVerdict[];
}

export interface CanaryService {
  actionId: string;
  baseline?: string;
  bindingId: string;
  candidate?: string;
  image?: string;
  name: string;
  provider: string;
}

export interface CanarySummary {
  actionIds: string[];
  deploymentId: string;
  finishedAt?: string;
  planId: string;
  reason?: string;
  services: string[];
  status: string;
}

export interface CanaryVerdict {
  actionId: string;
  attempt: number;
  baseline: Health;
  baselineFallback: boolean;
  candidate: Health;
  decidedAt: string;
  deploymentId: string;
  id: string;
  orgId: string;
  projectId: string;
  reason: string;
  retry: number;
  service: string;
  step: number;
  thresholds: Thresholds;
  verdict: string;
  weight: number;
  windowEnd: string;
  windowStart: string;
}

export interface CancelResult {
  message?: string;
}

export interface CapacityAccountSnapshot {
  accountId: string;
  checkedAt: string;
  provider: string;
  provisioned: CapacityResources;
  quota: CapacityQuota;
}

export interface CapacityBlock {
  cloudId: string;
  counted: number;
  limit: number;
  policyRevision: number;
  poolId: string;
  reason: string;
  requested: number;
  resource: string;
  retryable: boolean;
  targetId: string;
}

export interface CapacityBudget {
  horizonSeconds: number;
  maxHorizonMicroUsd: string;
  maxHourlyMicroUsd: string;
}

export interface CapacityChange {
  cloudId: string;
  entityId: string;
  kind: string;
  orgId: string;
  revision: number;
  status?: string;
}

export interface CapacityDemand {
  architecture: string;
  cloudId: string;
  environmentId: string;
  intentHash: string;
  localData: boolean;
  orgId: string;
  planId: string;
  policyRevision: number;
  poolId: string;
  projectId: string;
  resources: ResourceVector;
  service: string;
  stickyServerId: string;
  targetId: string;
}

export interface CapacityIntent {
  assignmentRevision: number;
  cloudRevision: number;
  commitSha: string;
  demand: CapacityDemand;
  grantRevision: number;
  targetName: string;
  targetRevision: number;
  template: PoolExpansionPolicy;
  templateHash: string;
}

export interface CapacityObservation {
  checkedAt: string;
  cpuMillis?: number;
  freeStorageBytes?: number;
  memoryBytes?: number;
  storageBytes?: number;
}

export interface CapacityPolicy {
  budget?: CapacityBudget;
  cloudId: string;
  enabled: boolean;
  frontendLimits: FrontendLimit[];
  gpuLimits?: GPULimit[];
  limits: ResourceVector;
  orgId: string;
  pools: PoolExpansionPolicy[];
  requireExpansionApproval: boolean;
  revision: number;
}

export interface CapacityPolicyInputBody {
  expectedRevision: number;
  policy: {
  budget?: CapacityBudget;
  cloudId: string;
  enabled: boolean;
  frontendLimits: FrontendLimit[];
  gpuLimits?: GPULimit[];
  limits: ResourceVector;
  orgId: string;
  pools: PoolExpansionPolicy[];
  requireExpansionApproval: boolean;
  revision: number;
};
}

export interface CapacityPolicyView {
  automaticExpansion: boolean;
  cloudId: string;
  policy?: CapacityPolicy;
}

export interface CapacityQuota {
  cpuMillis?: number;
  memoryBytes?: number;
  servers?: number;
  storageBytes?: number;
}

export interface CapacityRecord {
  accountId: string;
  checkedAt: string;
  clusterCheckedAt?: string;
  clusterComplete?: boolean;
  clusterConditions?: ClusterCondition[];
  clusterNode?: CloudinfraClusterNodeCapacity;
  clusterResourceId?: string;
  gpuKind?: string;
  gpus?: number;
  kind: string;
  observation?: CapacityObservation;
  provider: string;
  resourceId: string;
  resources: CapacityResources;
  serverId?: string;
  serverStateRevision: number;
}

export interface CapacityResources {
  cpuMillis?: number;
  memoryBytes?: number;
  servers?: number;
  storageBytes?: number;
}

export interface CapacityUsage {
  accounts: CapacityAccountSnapshot[];
  allocatable: CapacityResources;
  checkedAt: string;
  cloudId: string;
  fresh: boolean;
  frontends: FrontendUsage[];
  gpus: GPUUsage[];
  headroom: CapacityResources;
  observed: CapacityResources;
  observedStorageBytes: number | null;
  pending: CapacityResources;
  provisioned: CapacityResources;
  records: CapacityRecord[];
  reserved: CapacityResources;
  systemReserve: CapacityResources;
}

export interface CapacityWait {
  blocks: CapacityBlock[];
  createdAt: string;
  revision: number;
}

export interface CapacityWaitInputBody {
  expectedRevision: number;
}

export interface Card {
  markdown: string;
  note?: string;
  sourceUrl: string;
}

export interface CatalogKindList {
  kinds: KindInfo[];
}

export interface CatalogconnectCreateInputBody {
  dsn: string;
  engine: string;
  name: string;
}

export interface CatalogconnectEndpoint {
  createdAt: string;
  createdBy: string;
  database?: string;
  engine: string;
  host: string;
  id: string;
  name: string;
  orgId: string;
  port: number;
  revision: number;
  tls: string;
  username?: string;
}

export interface CatalogproxySession {
  expiresAt: string;
  url: string;
}

export interface ChallengeCredentialInputBody {
  connectionId: string;
  revision: number;
}

export interface Change {
  field: string;
  from: string;
  to: string;
}

export interface ChangeInput {
  cloudId: string;
  connectionId?: string;
  default?: boolean;
  environmentId?: string;
  poolId?: string;
  provider?: string;
  region?: string;
  revision: number;
  runtime?: string;
  targetId?: string;
}

export interface ChangeInputBody {
  cloudId: string;
  connectionId?: string;
  default?: boolean;
  environmentId?: string;
  poolId?: string;
  provider?: string;
  region?: string;
  revision: number;
  runtime?: string;
  targetId?: string;
}

export interface ChangedService {
  actionId: string;
  canary: boolean;
  lastCanary: string;
  name: string;
  op: string;
  provider: string;
}

export interface CheckInputBody {
  dependency: string;
  dsn: string;
  engine: string;
  kind: string;
  target: string;
}

export interface CheckResult {
  missing?: string[];
  notes?: string[];
  ok: boolean;
  problems?: string[];
  version?: string;
}

export interface ChooseConnectionInputBody {
  connectionId: string;
}

export interface CleanupBlock {
  reason: string;
  until: string;
}

export interface CleanupObject {
  name: string;
  namespace: string;
  resource: string;
}

export interface CleanupProblem {
  code: string;
  message: string;
  retryable: boolean;
}

export interface CleanupReview {
  blockers?: string[];
  connectionId: string;
  deploymentId: string;
  effect: string;
  previousReviewId?: string;
  provider: string;
  region: string;
  resource: string;
  reviewSequence?: number;
  revision: string;
  targets?: CleanupTarget[];
  variant?: string;
}

export interface CleanupTarget {
  deploymentId: string;
  identityCurrent: boolean;
  kind: string;
  namespace: string;
  objects: CleanupObject[];
  planId: string;
  service: string;
  unresolvedWrites: number;
}

export interface ClientClusterNodeCapacity {
  allocatable: ClientClusterResourceMetrics;
  architecture: string;
  poolId: string;
  ready: boolean;
  requests: ClientClusterResourceMetrics;
  taints: NodePoolTaint[];
  unschedulable: boolean;
  zone: string;
}

export interface ClientClusterResourceMetrics {
  cpuMillis?: number;
  ephemeralStorageBytes?: number;
  extended: Record<string, any>;
  memoryBytes?: number;
  pods?: number;
}

export interface CloseInputBody {
  reason?: string;
}

export interface CloudAdoption {
  bindingId: string;
  externalId: string;
  planId: string;
  revision: string;
}

export interface CloudCluster {
  accountId: string;
  cloudId: string;
  connectionId: string;
  desiredRevision: number;
  externalId: string;
  id: string;
  lifecycleAuthority: string[];
  observation?: ClusterObservation;
  observedRevision: number;
  ownership: string;
  provider: string;
  retainedPriceCheckedAt: string;
  retainedResources?: RetainedResource[];
  revision: number;
  scalingPolicies?: Record<string, any>;
  status: string;
  updatedAt: string;
}

export interface CloudClusterOperation {
  attempt: number;
  budgetReview?: BudgetReview;
  cancelRequested: boolean;
  cloudId: string;
  createdAt: string;
  digest: string;
  enrollment?: ClusterEnrollmentSummary;
  envelopeReview?: ClusterEnvelopeReview;
  error: string;
  estimatedNodeHourlyMicroUsd: number;
  expiresAt: string;
  id: string;
  input: ClusterInput;
  kind: string;
  moneyEstimate?: MoneyEstimate;
  resourceId: string;
  revision: number;
  status: string;
  steps: ClusterStep[];
  unknownCosts: string[];
  updatedAt: string;
}

export interface CloudClusterOperationPage {
  items: CloudClusterOperation[];
  nextCursor?: string;
}

export interface CloudClusterPage {
  items: CloudCluster[];
  nextCursor?: string;
}

export interface CloudCompensation {
  actionId: string;
  originalCompleted: boolean;
  planId: string;
  retained: boolean;
  routes?: CloudinfraRoute[];
  serverAttempt: boolean;
}

export interface CloudForm {
  builtin: boolean;
  cloud: string;
  credential?: string;
  help?: string;
  inClusterSettings?: Setting[];
  keylessSettings?: Setting[];
  methods: string[];
  settings?: Setting[];
  signInSettings?: Setting[];
  title: string;
}

export interface CloudLocation {
  accountId?: string;
  checkedAt: string;
  cidrs?: string[];
  cloudId: string;
  connectionId?: string;
  evidenceRef?: string;
  evidenceRevision: number;
  evidenceSource?: string;
  failureDomains: FailureDomain[];
  id: string;
  name: string;
  networkRefs?: string[];
  orgId: string;
  provider: string;
  region: string;
  revision: number;
  verifiedBy?: string;
}

export interface CloudLocationDeleteInputBody {
  cloudRevision: number;
  revision: number;
}

export interface CloudLocationList {
  locations: CloudLocation[];
}

export interface CloudPlacement {
  accountIdentity: string;
  allocation?: string;
  assignmentRevision: number;
  cloudId: string;
  cloudRevision: number;
  connectionGeneration?: number;
  connectionId: string;
  cpuMillis?: number;
  default?: boolean;
  gpuKind?: string;
  gpus?: number;
  grantRevision?: number;
  memoryBytes?: number;
  poolId?: string;
  poolRevision?: number;
  provider: string;
  region?: string;
  regionSource?: string;
  serverId?: string;
  serverRevision?: number;
  targetId: string;
  targetName: string;
  targetRevision: number;
}

export interface CloudProjectGrantInputBody {
  targetIds: string[];
}

export interface CloudProjectGrantList {
  grants: (ProjectGrant | null)[];
}

export interface CloudSQLApplicationNetwork {
  consumerProject: string;
  network: string;
  subnetwork: string;
}

export interface CloudSQLRecoveryNetwork {
  consumerProject: string;
  network: string;
}

export interface CloudSQLSourceEndpoint {
  consumerProject: string;
  forwardingRule: string;
  network: string;
  provisionSubnetwork?: string;
}

export interface CloudSyncEvent {
  attempt: number;
  cloudId: string;
  environments: EnvironmentChange[];
  id: string;
  orgId: string;
  projectId: string;
  reverts?: string;
  status: string;
  targetId: string;
  updatedAt: string;
}

export interface CloudSyncList {
  next?: string;
  syncs: (Sync | null)[];
}

export interface CloudTargetList {
  targets: (CloudprofilesTarget | null)[];
}

export interface CloudinfraClusterNodeCapacity {
  allocatable: CloudinfraClusterResourceMetrics;
  architecture: string;
  externalVmId: string;
  gpuKinds: Record<string, any>;
  nodeUid: string;
  poolId: string;
  provenance: Record<string, any>;
  provisioned: CapacityResources;
  ready: boolean;
  requests: CloudinfraClusterResourceMetrics;
  taints: NodePoolTaint[];
  unschedulable: boolean;
  zone: string;
}

export interface CloudinfraClusterResourceMetrics {
  cpuMillis?: number;
  ephemeralStorageBytes?: number;
  extended: Record<string, any>;
  memoryBytes?: number;
  pods?: number;
}

export interface CloudinfraMoveInputBody {
  architecture?: string;
  destinationServerId: string;
  environmentId: string;
  poolId: string;
  projectId: string;
  review?: string;
  service: string;
}

export interface CloudinfraRoute {
  binding: RouteBinding;
  id: string;
  input: RouteInput;
  operationId: string;
  orgId: string;
  projectId: string;
  receipt?: DNSRecord;
  receipts?: DNSRecord[];
  revision: number;
  status: string;
  updatedAt: string;
  verification: RouteVerification;
}

export interface CloudinfraRouteStatus {
  backend: string;
  certificate: string;
  certificateExpiresAt: string;
  checkedAt: string;
  dns: string;
  hostname: string;
  message?: string;
  operationId?: string;
  retryable: boolean;
  route: string;
  routeId: string;
  wildcard: boolean;
}

export interface CloudprofilesCreateInputBody {
  defaultPlacement?: {
  connectionId: string;
  provider: string;
  region: string;
};
  name: string;
}

export interface CloudprofilesTarget {
  cloudId: string;
  connectionId: string;
  createdAt: string;
  displayName: string;
  id: string;
  locationId?: string;
  managedResourceId?: string;
  name: string;
  orgId: string;
  poolId?: string;
  projectId: string;
  provider: string;
  region: string;
  revision: number;
  runtime: string;
  tags: string[];
  updatedAt: string;
}

export interface CloudprofilesUpdateInputBody {
  name: string;
  revision: number;
}

export interface ClusterCapacity {
  checkedAt: string;
  complete: boolean;
  conditions: ClusterCondition[];
  fresh: boolean;
  nodes: ClientClusterNodeCapacity[];
  observationRevision: number;
  resourceId: string;
  workloadsComplete: boolean;
}

export interface ClusterChange {
  cloudId: string;
  operationId: string;
  reason: string;
  resourceId: string;
  revision: number;
  status: string;
}

export interface ClusterCondition {
  reason?: string;
  status: string;
  type: string;
}

export interface ClusterDemand {
  maximum: ClusterWorkloadVector;
  mode: string;
  profile?: ClusterSchedulingProfile;
}

export interface ClusterEnrollmentSummary {
  attachmentId: string;
  credentialExpiry: string;
  runtimeConnectionId: string;
  runtimeVerified: boolean;
  targetId: string;
}

export interface ClusterEnvelopeResources {
  cpuMillis: number;
  gpuKind?: string;
  gpus?: number;
  memoryBytes: number;
  servers: number;
  storageBytes: number;
}

export interface ClusterEnvelopeReview {
  automatic: boolean;
  pools: ClusterPoolEnvelopeReview[];
}

export interface ClusterInput {
  accountId: string;
  acknowledgeNodeCosts: boolean;
  acknowledgeUnknownCosts: boolean;
  attachment?: AttachmentInput;
  cloudRevision: number;
  connectionGeneration: number;
  connectionId: string;
  kind: string;
  maintenance?: ClusterMaintenanceInput;
  managementReachable: boolean;
  optionsCheckedAt: string;
  optionsDigest: string;
  policyRevision: number;
  provider: string;
  ref: ClusterRef;
  resourceId?: string;
  resourceRevision: number;
  retirement?: ClusterRetirementInput;
  runtimeAccess?: RuntimeAccessReview;
  scaling?: ClusterScalingInput;
  spec: ClusterSpec;
  workerImage: string;
}

export interface ClusterInputBody {
  accountId: string;
  acknowledgeNodeCosts: boolean;
  acknowledgeUnknownCosts: boolean;
  attachment?: {
  adoptNamespace: boolean;
  cloudRevision: number;
  endpointFingerprint: string;
  grantRevision: number;
  namespace: string;
  namespaceUid?: string;
  prerequisites: AttachmentPrerequisites;
  projectId: string;
  resourceRevision: number;
  runtimeAccess?: RuntimeAccessProfile;
  serviceAccountName: string;
  targetSlug: string;
  trustFingerprint: string;
};
  cloudRevision: number;
  connectionGeneration: number;
  connectionId: string;
  kind: string;
  maintenance?: {
  drainDigest?: string;
  observationDigest: string;
  observedRevision: number;
  pool?: NodePoolSpec;
  poolId?: string;
  update?: ClusterUpdate;
  version?: string;
  window?: ClusterMaintenanceWindow;
};
  managementReachable: boolean;
  optionsCheckedAt: string;
  optionsDigest: string;
  policyRevision: number;
  provider: string;
  ref: {
  accountId: string;
  id: string;
  incarnation: string;
  region: string;
};
  resourceId?: string;
  resourceRevision: number;
  retirement?: {
  attachmentId?: string;
  expectedAttachmentRevision?: number;
  reviewDigest: string;
};
  runtimeAccess?: {
  attachmentId: string;
  attachmentRevision?: number;
  capabilityRevision?: number;
  namespace?: string;
  profile: RuntimeAccessProfile;
  projectId?: string;
};
  scaling?: {
  continuityFloor: number;
  continuityPoolId?: string;
  cooldownSeconds: number;
  expectedRevision: number;
  maxNodes: number;
  minNodes: number;
  mode: string;
  poolId: string;
};
  spec: {
  ha: boolean;
  maintenance: ClusterMaintenance;
  name: string;
  networkId: string;
  nodePools: NodePoolSpec[];
  providerOptions?: ClusterProviderOptions;
  region: string;
  version: string;
};
  workerImage: string;
}

export interface ClusterMaintenance {
  day: string;
  startTime: string;
}

export interface ClusterMaintenanceInput {
  drainDigest?: string;
  observationDigest: string;
  observedRevision: number;
  pool?: NodePoolSpec;
  poolId?: string;
  update?: ClusterUpdate;
  version?: string;
  window?: ClusterMaintenanceWindow;
}

export interface ClusterMaintenanceWindow {
  durationSeconds: number;
  endUtc: string;
  localStart: string;
  startUtc: string;
  timezone: string;
}

export interface ClusterNodeSize {
  cpus: number;
  diskGb: number;
  hourlyUsd?: number;
  id: string;
  memoryMib: number;
  monthlyUsd?: number;
  regions: string[];
}

export interface ClusterObservation {
  caHash: string;
  conditions: ClusterCondition[];
  endpoint: string;
  ha?: boolean;
  maintenance?: ClusterMaintenance;
  name: string;
  observedAt: string;
  pools: NodePoolSpec[];
  ref: ClusterRef;
  state: string;
  version: string;
}

export interface ClusterOptions {
  accountId: string;
  checkedAt: string;
  connectionGeneration: number;
  digest: string;
  networks: string[];
  nodeSizeFacts: ClusterNodeSize[];
  nodeSizes: string[];
  providerOptions?: ClusterProviderOptions;
  regions: string[];
  versions: string[];
}

export interface ClusterPage {
  items: ClusterObservation[];
  nextCursor?: string;
}

export interface ClusterPoolEnvelopeReview {
  baseline: ClusterEnvelopeResources;
  initial: ClusterEnvelopeResources;
  maximum: ClusterEnvelopeResources;
  money: MoneyEstimate;
  poolId: string;
}

export interface ClusterProblem {
  categories?: string[];
  code: string;
  message: string;
  resource?: string;
  retryable: boolean;
}

export interface ClusterProviderOptions {
  payload: string;
  provider: string;
  version: number;
}

export interface ClusterRef {
  accountId: string;
  id: string;
  incarnation: string;
  region: string;
}

export interface ClusterRetirementInput {
  attachmentId?: string;
  expectedAttachmentRevision?: number;
  reviewDigest: string;
}

export interface ClusterRetirementPreviewInputBody {
  attachmentId?: string;
  expectedAttachmentRevision?: number;
}

export interface ClusterRetirementReview {
  blockers: string[];
  checkedAt: string;
  deletionCandidates: RetainedResource[];
  digest: string;
  estimatedRemainingHourlyMicroUsd: string;
  resourceId: string;
  retainedResources: RetainedResource[];
  unknownCosts: string[];
}

export interface ClusterScalingInput {
  continuityFloor: number;
  continuityPoolId?: string;
  cooldownSeconds: number;
  expectedRevision: number;
  maxNodes: number;
  minNodes: number;
  mode: string;
  poolId: string;
}

export interface ClusterSchedulingProfile {
  allocatable: ClusterWorkloadVector;
  architecture: string;
  available: ClusterWorkloadVector;
  digest: string;
  overhead: ClusterWorkloadVector;
  poolId: string;
  templateDigest: string;
  zone: string;
}

export interface ClusterSpec {
  ha: boolean;
  maintenance: ClusterMaintenance;
  name: string;
  networkId: string;
  nodePools: NodePoolSpec[];
  providerOptions?: ClusterProviderOptions;
  region: string;
  version: string;
}

export interface ClusterStep {
  attemptedAt?: string;
  completedAt?: string;
  name: string;
  state: string;
}

export interface ClusterUpdate {
  fieldMask: string[];
  ha: boolean;
  maintenance: ClusterMaintenance;
  providerOptions?: ClusterProviderOptions;
}

export interface ClusterWorkloadVector {
  cpuMillis: number;
  ephemeralStorageBytes: number;
  memoryBytes: number;
  pods: number;
}

export interface Collection {
  actionId: string;
  attempt: string;
  deploymentId: string;
  id: string;
  message: string;
  operationId: string;
  orgId: string;
  projectId: string;
  state: string;
  updatedAt: string;
}

export interface Commands {
  build?: string;
  dev?: string;
  start?: string;
}

export interface Compatibility {
  controlVersions?: string[];
  readDataVersions?: string[];
  writeDataVersion?: string;
}

export interface CompleteCloudRoutingInputBody {
  environmentId: string;
  evidence: string;
}

export interface CompleteInputBody {
  code: string;
  state: string;
}

export interface ComponentConfig {
  component: ResolvedComponent;
  dependencies?: Record<string, any>;
  gpu?: GPUPlacement;
  inputs?: string;
  kind: string;
  protection: string;
  resource: string;
  revision: string;
  settings?: Record<string, any>;
}

export interface ComponentInfo {
  name: string;
  protection: string;
  role: string;
  stateful: boolean;
}

export interface CompositionConfig {
  dependencies?: Record<string, any>;
  kind: string;
  outputs?: Output[];
  revision: string;
  secrets?: GeneratedSecret[];
  version: string;
}

export interface CompromisedRevoke {
  joinedNodes: Node[];
  joinedNodesWithheld: boolean;
  revoked: APIKey;
}

export interface Concern {
  at: string;
  kind: string;
  runId: string;
  text: string;
}

export interface ConnectGitHubInputBody {
  code: string;
  installationId: number;
  state: string;
}

export interface ConnectGitLabInputBody {
  code: string;
  state: string;
}

export interface ConnectedBitbucketRepository {
  defaultBranch: string;
  fullName: string;
  private: boolean;
  workspace: string;
}

export interface ConnectedRepository {
  defaultBranch: string;
  fullName: string;
  installation: string;
  private: boolean;
  url: string;
}

export interface ConnectedRepositoryList {
  repositories: ConnectedRepository[];
}

export interface Connection {
  account: string;
  accountIdentity?: string;
  checkedAt?: string;
  cloud: string;
  createdAt: string;
  createdBy: string;
  details: Record<string, any>;
  generation: number;
  id: string;
  message: string;
  method: string;
  missing: string[];
  name: string;
  orgId: string;
  signedInBy?: string;
  status: string;
  updatedAt: string;
}

export interface ConnectionList {
  connections: Connection[];
}

export interface ConnectionsCloudList {
  clouds: CloudForm[];
}

export interface ConnectionsCreateInputBody {
  cloud: string;
  credential: string;
  name: string;
  settings?: Record<string, any>;
}

export interface ConsumerKey {
  createdAt?: string;
  lastUsedAt?: string;
  lastUsedKnown: boolean;
  prefix?: string;
  revokedAt?: string;
  service: string;
  state: string;
  tenant: string;
}

export interface ConsumerOption {
  name: string;
  pattern?: string;
  required?: boolean;
  title: string;
}

export interface Conversation {
  createdAt: string;
  id: string;
  lastTaskId: string;
  orgId: string;
  pinned: boolean;
  pinnedAt?: string;
  projectId: string;
  startedBy: string;
  status: string;
  title: string;
  turns: number;
  updatedAt: string;
}

export interface ConversationList {
  conversations: Conversation[];
}

export interface ConversationUpdateBody {
  pinned?: boolean;
  title?: string;
}

export interface Cost {
  estimate: boolean;
  hourlyUsd: string;
  known: boolean;
  monthlyUsd: string;
  share: string;
  source: string;
}

export interface Coverage {
  canary: string[];
  duration: number;
  environment: string;
  services: ServiceCoverage[];
  unverified: boolean;
}

export interface CreateEnvironmentInputBody {
  kind?: string;
  name: string;
}

export interface CreateInstanceInputBody {
  externalRef: string;
  idempotencyKey: string;
  parameters?: Record<string, any>;
  profileId: string;
  releaseId: string;
  secretRefs?: Record<string, any>;
  stageId: string;
}

export interface CreateInvitationInputBody {
  email: string;
  role?: string;
}

export interface CreatePlanInputBody {
  catalog?: boolean;
  environment: string;
  from?: string;
  scope?: string[];
  services?: string[];
}

export interface CreateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name: string;
  repositories?: {
  alias?: string;
  ref?: string;
  repository: string;
  root?: string;
}[];
  repositoryUrl?: string;
  slug?: string;
}

export interface CreateTargetInputBody {
  connectionId: string;
  displayName: string;
  locationId?: string;
  name: string;
  poolId?: string;
  provider: string;
  region: string;
  runtime: string;
  tags?: string[];
}

export interface CreateTeamInputBody {
  description?: string;
  name: string;
}

export interface CreateVariableInputBody {
  environment?: string;
  key: string;
  secret?: boolean;
  service?: string;
  value: string;
}

export interface CreatedInvitation {
  acceptPath: string;
  emailMessage: string;
  emailStatus: string;
  invitation: OrgInvitation;
}

export interface CreatedKey {
  key: APIKey;
  secret: string;
}

export interface DNSRecord {
  id: string;
  name: string;
  owner?: string;
  proxied?: boolean;
  ttl: number;
  type: string;
  value: string;
  zoneId: string;
}

export interface DNSZone {
  accountId: string;
  id: string;
  name: string;
  nameservers?: string[];
  verified: boolean;
}

export interface DatabaseGateway {
  cloudTags?: string[];
  environments?: Record<string, any>;
  provider?: string;
  region?: string;
}

export interface DatabaseGatewayConfig {
  name: string;
  program: string;
  resources: string[];
  suffix: string;
  trust?: string;
}

export interface DbgatewayRouteStatus {
  backendConns: number;
  id: string;
  maxConnections: number;
  pinRate: number;
  reachable: boolean;
  reported: boolean;
  resource: string;
  state: string;
}

export interface DecideInputBody {
  note?: string;
  revision?: string;
}

export interface DeclineInputBody {
  reason?: string;
}

export interface DefaultPlacement {
  connectionId: string;
  provider: string;
  region: string;
}

export interface Defaults {
  cloudTags?: string[];
  provider?: string;
  region?: string;
}

export interface DeletionPreview {
  blocked?: string;
  removes: string[];
}

export interface Delivery {
  attempts: number;
  createdAt: string;
  deliveredAt?: string;
  eventType: string;
  id: string;
  nextRetryAt?: string;
  status: string;
  statusCode: number;
}

export interface DeliveryList {
  items: Delivery[];
}

export interface DependencyInfo {
  default: boolean;
  external: boolean;
  kinds: string[];
  name: string;
  optional: boolean;
  title: string;
}

export interface DeployInputBody {
  entryId: string;
  maxModelLen?: number;
  monthlyBudgetUsd?: string;
  quantization?: string;
  resource?: string;
  reviewServer?: boolean;
  server?: {
  cloudId: string;
  operationId: string;
  review: string;
};
  target: {
  connectionId: string;
  gpu?: GPURequest;
  provider?: string;
  region?: string;
  sizeId?: string;
  source: string;
};
}

export interface DeployPreview {
  addsTo?: string;
  canDeploy: boolean;
  cost: Cost;
  entry: EntrySummary;
  environments: Reach[];
  fit: Fit;
  gated: boolean;
  gpu?: GPURequest;
  kind: string;
  maxModelLen: number;
  problems: Problem[];
  quantization: string;
  resource: string;
  server?: Operation;
  serverReviewNeeded?: boolean;
  target: TargetChoice;
  targetName: string;
  tokenReady: boolean;
  version: string;
}

export interface DeployResult {
  deployment?: ModelsDeployment;
  message?: string;
  planId?: string;
}

export interface DeployedList {
  sources: DeployedRepository[];
}

export interface DeployedRepository {
  alias: string;
  commitSha: string;
  deployedAt: string;
  planId: string;
  repository: string;
  sourceId: string;
}

export interface Deployment {
  canary?: Canary;
  cancelRequestedAt?: string;
  capacityWait?: CapacityWait;
  commitSha: string;
  environment: string;
  environmentId: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  planId: string;
  projectId: string;
  retries: number;
  sources: SourceArtifact[];
  startedAt: string;
  startedBy: string;
  status: string;
  steps: Step[];
}

export interface DeploymentCounts {
  failed: number;
  medianSeconds: number;
  retried: number;
  running: number;
  succeeded: number;
}

export interface DeploymentList {
  deployments: DeploymentsDeployment[];
  next?: string;
}

export interface DeploymentsDeployment {
  canary?: Canary;
  cancelRequestedAt?: string;
  capacityWait?: CapacityWait;
  commitSha: string;
  environment: string;
  environmentId: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  planId: string;
  projectId: string;
  retries: number;
  sources: SourceArtifact[];
  startedAt: string;
  startedBy: string;
  status: string;
  steps: Step[];
}

export interface DiffCommit {
  at: string;
  author: string;
  sha: string;
  title: string;
}

export interface DiscoveredService {
  error?: string;
  metadata?: Record<string, any>;
  optedIn: boolean;
  routes: number;
  service: string;
  state: string;
  updatedAt: string;
}

export interface DiscoveryEvidence {
  detail: string;
  line?: number;
  path: string;
  source?: string;
}

export interface DiscoverySource {
  alias: string;
  createdAt: string;
  createdBy: string;
  id: string;
  orgId: string;
  projectId: string;
  push?: PushState;
  ref: string;
  removedAt?: string;
  repository: string;
  root: string;
  updatedAt: string;
}

export interface DiscoverySummary {
  lastChange: string;
  mode: string;
  namespace?: string;
  services: DiscoveredService[];
}

export interface DomainCheckInputBody {
  connectionId: string;
  domains: string[];
}

export interface DomainDetail {
  domain: ZoneOverview;
  parent?: ZoneOverview;
  proofZone?: ZoneOverview;
  records: ManagedRecord[];
  registration?: Registration;
  subdomains: ZoneOverview[];
}

export interface DomainOffer {
  name: string;
  price?: DomainPrice;
  reason?: string;
  registrable: boolean;
  tier?: string;
}

export interface DomainOfferList {
  offers: DomainOffer[];
}

export interface DomainOperationList {
  operations: (RouteOperation | null)[];
}

export interface DomainOverview {
  registrations: (Registration | null)[];
  zones: ZoneOverview[];
}

export interface DomainPrice {
  currency: string;
  registrationCost: string;
  renewalCost: string;
}

export interface DomainRouteList {
  routes: (CloudinfraRoute | null)[];
}

export interface DrainPolicy {
  maxConnectionLifetimeSeconds?: number;
  mode: string;
  terminationGraceSeconds: number;
}

export interface DrainReview {
  reservations: number;
  review: string;
  serverId: string;
  serverRevision: number;
  usage?: ServerUsage;
}

export interface DriftCounts {
  changed: number;
  deployed: number;
  missing: number;
}

export interface DurableObjectClass {
  className: string;
  name: string;
}

export interface Edge {
  confidence: string;
  evidence: DiscoveryEvidence[];
  from: string;
  kind: string;
  to: string;
}

export interface EdgeInputBody {
  policy: {
  enabled: boolean;
  invalidateOnDeploy: boolean;
  rules: EdgeRule[];
};
  revision: number;
}

export interface EdgePolicy {
  enabled: boolean;
  invalidateOnDeploy: boolean;
  rules: EdgeRule[];
}

export interface EdgePurgeInputBody {
  revision: number;
  urls: string[];
}

export interface EdgeRule {
  browserTtl: number;
  edgeTtl: number;
  mode: string;
  pathPrefix: string;
}

export interface EdgeState {
  appliedAt: string;
  desired?: DNSRecord;
  invalidationError?: string;
  kind: string;
  lastDeploymentId?: string;
  lastPurgeAt: string;
  message: string;
  orgId: string;
  policy: EdgePolicy;
  prior?: DNSRecord;
  projectId: string;
  requestedAt: string;
  revision: number;
  routeId: string;
  status: string;
  urls?: string[];
}

export interface EditInputBody {
  baseRevision?: number;
  addResource?: {
  enabledIn?: string;
  existingResource?: string;
  gpu?: ManifestGPU;
  kind: string;
  name: string;
  provider?: string;
  region?: string;
  settings?: Record<string, any>;
  target?: string;
  uses?: Record<string, any>;
  version?: string;
};
  ignore?: string;
  link?: {
  need: string;
  options?: Record<string, any>;
  service: string;
};
  placement?: {
  build?: Build;
  cloudSqlNetwork?: CloudSQLApplicationNetwork;
  cloudTags?: string[];
  cloudTarget?: string;
  enabled?: boolean;
  endpoint?: ManifestEndpoint;
  environment?: string;
  healthPath?: string;
  name: string;
  provider?: string;
  region?: string;
  scale?: Scale;
  section: string;
  size?: SizeEdit;
  target?: string;
};
  removeResource?: string;
  setUse?: {
  dependency: string;
  resource: string;
  use: ManifestUse;
};
  unlink?: {
  need: string;
  options?: Record<string, any>;
  service: string;
};
  updateResource?: {
  clearGpu?: boolean;
  enabled?: boolean;
  environment?: string;
  gpu?: ManifestGPU;
  provider?: string;
  resource: string;
  settings?: Record<string, any>;
  target?: string;
};
}

export interface EffectiveView {
  baseUrl?: string;
  model: string;
  provider: string;
  summaryModel: string;
}

export interface Embeddings {
  deployment?: string;
  dimensions: number;
  mode: string;
  model: string;
  provider?: string;
}

export interface EnableBitbucketWebhookInputBody {
  repository: string;
}

export interface EndpointList {
  endpoints: (CatalogconnectEndpoint | null)[];
}

export interface EnrollmentBody {
  hostKey: string;
}

export interface EntryDetail {
  contextLength: number;
  cpu: boolean;
  family: string;
  fitsHere?: boolean;
  fitsNotice?: string;
  gated: boolean;
  huggingFaceUrl: string;
  id: string;
  license: LicenseInfo;
  minGpuMemoryGib: number;
  parameters: number;
  publisher: string;
  quantizations: QuantizationView[];
  queryPrefix?: string;
  revision: string;
  summary: string;
  tasks: string[];
  testedOn: string[];
  title: string;
  verification: string;
}

export interface EntrySummary {
  contextLength: number;
  cpu: boolean;
  family: string;
  fitsHere?: boolean;
  gated: boolean;
  id: string;
  license: LicenseInfo;
  minGpuMemoryGib: number;
  parameters: number;
  publisher: string;
  queryPrefix?: string;
  summary: string;
  tasks: string[];
  testedOn: string[];
  title: string;
  verification: string;
}

export interface EnvLink {
  address?: string;
  field?: string;
  from: string;
  gateway?: string;
  key: string;
  options?: Record<string, any>;
  subject: string;
}

export interface EnvUse {
  confidence: string;
  evidence: DiscoveryEvidence[];
  example?: string;
  name: string;
}

export interface EnvVar {
  from?: Ref;
  name: string;
  requires?: string;
  secret?: string;
  template?: string;
  value?: string;
}

export interface Environment {
  createdAt: string;
  id: string;
  instanceId?: string;
  kind: string;
  name: string;
  orgId: string;
  projectId: string;
  updatedAt: string;
}

export interface EnvironmentChange {
  compensationMessage?: string;
  compensationMode?: string;
  deploymentId?: string;
  environment: string;
  environmentId: string;
  error?: string;
  externalRouting: boolean;
  planId?: string;
  projectId?: string;
  routes?: SyncRoute[];
  routingEvidence?: string;
  routingStatus?: string;
  services: string[];
  status: string;
  summary: PlanSummary;
}

export interface EnvironmentCloud {
  assignment: Assignment | null;
  revision: number;
}

export interface EnvironmentCloudEvent {
  cloudId: string | null;
  environmentId: string;
  orgId: string;
  projectId: string;
  revision: number;
}

export interface EnvironmentCloudInputBody {
  cloudId: string;
  revision: number;
}

export interface EnvironmentConnections {
  connections: Record<string, any>;
  environment: string;
}

export interface EnvironmentList {
  environments: Environment[];
}

export interface EnvironmentUsage {
  assigned: boolean;
  environmentId: string;
  environmentName: string;
  placements: PlacementUsage[];
  projectId: string;
  projectName: string;
}

export interface Event {
  at: string;
  data: string;
  kind: string;
  seq: number;
  taskId: string;
}

export interface ExcludeInputBody {
  exclusions: {
  keepRunning?: boolean;
  kind: string;
  match: string;
  name?: string;
  reason: string;
}[];
}

export interface Excluded {
  rule: ExclusionRule;
  service: Service;
}

export interface Exclusion {
  createdAt: string;
  createdBy: string;
  keepRunning: boolean;
  kind: string;
  match: string;
  name?: string;
  reason: string;
}

export interface ExclusionRule {
  kind: string;
  match: string;
}

export interface ExistingResourceConfig {
  accountIdentity: string;
  endpointId: string;
  endpointRevision: number;
  externalId: string;
  incarnation: string;
  resourceId: string;
  revision: number;
}

export interface Expansion {
  accelerator?: Accelerator;
  accountId: string;
  approvedAt?: string;
  approvedBy: string;
  architecture: string;
  block?: CapacityBlock;
  cloudId: string;
  connectionGeneration: number;
  connectionId: string;
  createdAt: string;
  estimatedHourlyUsd?: number;
  hourlyPriceKnown?: boolean;
  id: string;
  image: string;
  intentHash: string;
  minimum: boolean;
  operationId: string;
  orgId: string;
  policyRevision: number;
  poolId: string;
  provider: string;
  region: string;
  resources: ResourceVector;
  revision: number;
  size: string;
  status: string;
  template: PoolExpansionPolicy;
  updatedAt: string;
}

export interface ExpansionList {
  expansions: Expansion[];
}

export interface FabriqKeyList {
  keys: ConsumerKey[];
}

export interface FabriqloomStatus {
  entityTypes: number;
  keysReportUse: boolean;
  kind: string;
  migrations: number;
  migrationsKnown: boolean;
  reachable: boolean;
  reason?: string;
  version: string;
}

export interface FabriqloomTenant {
  createdAt: string;
  id: string;
  label: string;
}

export interface Fact {
  label: string;
  path?: string;
  untrusted?: boolean;
  value: string;
}

export interface FailedCleanup {
  attempt: number;
  deploymentId: string;
  error?: string;
  errorCode?: string;
  reviewId?: string;
  reviewSequence?: number;
  revision: string;
  status: string;
  variant?: string;
}

export interface FailedCleanupCancelInputBody {
  expectedReviewId: string;
}

export interface FailedCleanupInputBody {
  revision: string;
}

export interface FailureDomain {
  dependencyIds?: string[];
  id: string;
  kind: string;
  source: string;
  verified: boolean;
}

export interface FarpConfig {
  gateway: string;
  metadata?: Record<string, any>;
  port: number;
}

export interface FarpHostnamesInputBody {
  hostnames: string[];
  revision: number;
}

export interface Features {
  ai: boolean;
}

export interface Federation {
  accessGrants?: Record<string, any>;
  destinations?: Record<string, any>;
  networkLinks?: Record<string, any>;
  placementGroups?: Record<string, any>;
  placements?: Record<string, any>;
  serviceExports?: Record<string, any>;
}

export interface FenceRepair {
  daemon: string;
  dirty: any;
  repaired: boolean;
  review: string;
  running: RunningExec[];
}

export interface FenceRepairInputBody {
  review: string;
}

export interface File {
  certificateSecret?: string;
  mode?: number;
  path: string;
}

export interface Finding {
  code: string;
  evidence?: DiscoveryEvidence[];
  message: string;
  name?: string;
  path?: string;
  subject?: string;
}

export interface Findings {
  confidence: string;
  likelyCause: string;
  suggestions: string[];
  summary: string;
  writtenAt: string;
}

export interface Fit {
  gpuKind: string;
  need: Need;
  reason?: string;
  result?: Result;
  status: string;
}

export interface Fleet {
  controlProjectId?: string;
  createdAt: string;
  id: string;
  orgId: string;
  projectId: string;
  revision: number;
  status: string;
  updatedAt: string;
}

export interface FleetOperation {
  createdAt: string;
  errorCode?: string;
  generation: number;
  id: string;
  instanceId: string;
  kind: string;
  requestHash: string;
  status: string;
}

export interface FrontendLimit {
  maximum?: number;
  targetId: string;
}

export interface FrontendUsage {
  active: number;
  counted: number;
  maximum?: number;
  pending: number;
  retained: number;
  targetId: string;
}

export interface GPUDevice {
  index: number;
  kind: string;
  memoryMib: number;
  product: string;
  uuid: string;
}

export interface GPUInventory {
  checkedAt: string;
  devices?: GPUDevice[];
  message?: string;
  nodes?: GPUNodeGroup[];
  status: string;
}

export interface GPULimit {
  kind: string;
  maximum: number;
}

export interface GPUNodeGroup {
  hourlyPriceKnown: boolean;
  hourlyUsd: number;
  instanceType?: string;
  kind: string;
  memoryGib: number;
  nodeSelector?: Record<string, any>;
  nodes: number;
  perNode: number;
  product?: string;
  tolerations?: GPUToleration[];
  total: number;
}

export interface GPUPlacement {
  count: number;
  kind: string;
  nodeSelector?: Record<string, any>;
  tolerations?: GPUToleration[];
}

export interface GPURequest {
  count: number;
  kind: string;
}

export interface GPUTarget {
  accelerator: Accelerator;
  cloud: string;
  code?: string;
  connectionId: string;
  fits: boolean;
  free: number;
  hourlyPriceKnown: boolean;
  hourlyUsd: number;
  message?: string;
  name: string;
  priceEstimate: boolean;
  regions?: string[];
  sizeId?: string;
  source: string;
}

export interface GPUTargetList {
  targets: GPUTarget[];
  unreachable: UnreachableAccount[];
}

export interface GPUToleration {
  effect: string;
  key: string;
  operator: string;
  value?: string;
}

export interface GPUUsage {
  kind: string;
  limit: number | null;
  pending: number;
  provisioned: number | null;
  reserved: number;
}

export interface GateCheck {
  detail: string;
  name: string;
  number: number;
  passed: boolean;
}

export interface GatewayList {
  gateways: GatewayStatus[];
}

export interface GatewayRoute {
  env: string;
  path: string;
  stripPrefix?: boolean;
  to: string;
}

export interface GatewayStatus {
  environmentId: string;
  id: string;
  instances: InstanceStatus[];
  name: string;
  provider: string;
  region: string;
  routes: DbgatewayRouteStatus[];
  snapshotVersion: number;
}

export interface GeneratedSecret {
  bytes?: number;
  encoding?: string;
  generator?: string;
  name: string;
  prefix?: string;
}

export interface GitHubInstallation {
  account: string;
  accountType: string;
  avatarUrl: string;
  createdAt: string;
  id: string;
  manageUrl: string;
}

export interface GitHubStatus {
  configured: boolean;
  installUrl?: string;
  installations: GitHubInstallation[];
}

export interface GitLabConnectionView {
  avatarUrl: string;
  createdAt: string;
  host: string;
  id: string;
  lapsed: boolean;
  username: string;
  version: string;
  versionSupported: boolean;
  webhookSet: boolean;
  webhookUrl: string;
}

export interface GitLabHostView {
  clientId: string;
  createdAt?: string;
  host: string;
  id: string;
  instance: boolean;
  webUrl: string;
}

export interface GitLabRepository {
  connection: string;
  defaultBranch: string;
  fullName: string;
  host: string;
  key: string;
  name: string;
  namespace: string;
  private: boolean;
}

export interface GitLabRepositoryList {
  repositories: GitLabRepository[];
}

export interface GitLabSignInStart {
  authorizeUrl: string;
}

export interface GitLabStatus {
  callbackUrl: string;
  canConnect: boolean;
  connections: GitLabConnectionView[];
  hosts: GitLabHostView[];
  organizationHosts: boolean;
}

export interface GitLabWebhookSecret {
  secretToken: string;
  signingToken: string;
  url: string;
}

export interface Graph {
  buildSystems: BuildSystem[];
  edges: Edge[];
  notes: Note[];
  packages: Package[];
  resources: Resource[];
  services: Service[];
  sources?: SourceRef[];
  version: number;
  workspaces: Workspace[];
}

export interface GraphSummary {
  deployable: number;
  needsConfirmation: number;
  packages: number;
  resources: number;
  services: number;
}

export interface GroupDestinationMappingInputBody {
  attachmentId: string;
  destinationId: string;
  environmentId: string;
  expectedRevision: number;
  targetId: string;
}

export interface GroupVerificationResult {
  id: string;
}

export interface HTTPProbe {
  checker?: string;
  path: string;
  port: string;
}

export interface HandoffInputBody {
  destinationCloudId: string;
  revision: number;
}

export interface HandoffReleaseInputBody {
  drainSeconds: number;
}

export interface Health {
  available: boolean;
  errors: number;
  latencyMs: number;
  reason?: string;
  requests: number;
  statistic?: string;
}

export interface Held {
  sameAddress?: string;
}

export interface HeldRoute {
  key: string;
  routeIds: string[];
  service: string;
}

export interface History {
  conversation: Conversation;
  more: boolean;
  turns: Turn[];
}

export interface Host {
  allotment: Allotment;
  clientFingerprint: string;
  endpoint: string;
  evidence: PlatformcloudEvidence;
  generation: number;
  hostId: string;
  id: string;
  regionId: string;
  revision: number;
  status: string;
}

export interface HostCard {
  attached: boolean;
  domainRouteId?: string;
  farpHome: boolean;
  hostname: string;
  routes: BoardRoute[];
  status?: CloudinfraRouteStatus;
}

export interface HostInputBody {
  allotment: {
  diskMib: number;
  memoryMib: number;
  vcpus: number;
  vms: number;
};
  endpoint: string;
  hostId: string;
  regionId: string;
  serverCa: string;
}

export interface HostList {
  hosts: HostView[];
}

export interface HostView {
  allotment: Allotment;
  clientFingerprint: string;
  endpoint: string;
  evidence: PlatformcloudEvidence;
  generation: number;
  hostId: string;
  id: string;
  placements: number;
  regionId: string;
  revision: number;
  status: string;
  use: Allotment;
}

export interface HostnameUse {
  environmentId: string;
  hostname: string;
  port: number;
  projectId: string;
  projectName: string;
  routeId: string;
  runsOn: string;
  service: string;
  status: CloudinfraRouteStatus;
}

export interface HuggingFaceToken {
  account: string;
  hint: string;
  revision: number;
  setAt: string;
}

export interface HuggingFaceTokenInputBody {
  token: string;
}

export interface IdentitySession {
  orgId: string;
  permissions: string[];
  sessionId?: string;
  userId: string;
}

export interface Ignore {
  resources?: string[];
}

export interface InClusterInputBody {
  name: string;
  settings: Record<string, any>;
}

export interface IncludeInputBody {
  exclusions: {
  kind: string;
  match: string;
}[];
}

export interface InfrastructureAction {
  id: string;
  kind: string;
  resourceId: string;
  status: string;
}

export interface InfrastructureStatus {
  engine: string;
  providers: ProviderStatus[];
  store: string;
}

export interface InspectInputBody {
  repo: string;
  revision?: string;
}

export interface InstallationIssues {
  accepted: boolean;
  account: string;
  accountType: string;
  checkedAt?: string;
  manageUrl: string;
}

export interface Instance {
  createdAt: string;
  desiredGeneration: number;
  desiredReleaseId: string;
  environmentId: string;
  externalRef: string;
  fleetId: string;
  id: string;
  observedGeneration: number;
  observedReleaseId?: string;
  ownershipEpoch: number;
  profileId: string;
  revision: number;
  stageId: string;
  status: string;
  updatedAt: string;
}

export interface InstancePage {
  items: Instance[];
  nextCursor?: string;
}

export interface InstanceRouteStats {
  backendConns: number;
  maxConnections: number;
  pinRate: number;
  pinned: number;
  reachable: boolean;
  routeId: string;
  waitP95Ms: number;
}

export interface InstanceStatus {
  behind: boolean;
  draining: boolean;
  id: string;
  lastSeenAt: string;
  reporting: boolean;
  routes: InstanceRouteStats[];
  snapshotVersion: number;
}

export interface Inventory {
  arch: string;
  cpus: number;
  diskGib: number;
  docker?: Runtime;
  endpoints?: string[];
  gpus?: NodeprotoGPU[];
  hostname?: string;
  kernel: string;
  memoryMib: number;
  os: string;
  podman?: Runtime;
  ports80443?: string;
  publicAddresses?: string[];
}

export interface Issue {
  error?: string;
  number?: number;
  state: string;
  url?: string;
}

export interface IssuesStatus {
  configured: boolean;
  installations: InstallationIssues[];
}

export interface JoinToken {
  command: string;
  expiresAt: string;
  token: string;
}

export interface JoinTokenList {
  tokens: PendingNodeJoinToken[];
}

export interface JoinTokenRequestBody {
  cloudId?: string;
  edge?: boolean;
  edgeScope?: string;
  endpoints?: string[];
  labels?: Record<string, any>;
  lifetimeMinutes?: number;
}

export interface KeyView {
  hint: string;
  name: string;
  type: string;
}

export interface KeylessInputBody {
  connectionId: string;
  name: string;
  settings: Record<string, any>;
}

export interface KeylessPrepareInputBody {
  cloud: string;
}

export interface KeylessReplaceInputBody {
  settings?: Record<string, any>;
}

export interface KeylessSetup {
  audience?: string;
  connectionId: string;
  expiresAt?: string;
  issuer: string;
  steps: SetupStep[];
  subject: string;
}

export interface KeylessState {
  enabled: boolean;
  issuer: string;
  reason: string;
}

export interface KindInfo {
  components?: ComponentInfo[];
  consumerOptions?: ConsumerOption[];
  dependencies?: DependencyInfo[];
  description?: string;
  engine: boolean;
  feature?: string;
  name: string;
  outputs?: OutputInfo[];
  placement: string;
  settings?: SettingInfo[];
  targets: string[];
  title: string;
  versions: string[];
}

export interface KnowledgeStatus {
  available: boolean;
  configured: boolean;
  reason?: string;
}

export interface KubernetesRule {
  apiGroups: string[];
  resources: string[];
  verbs: string[];
}

export interface Leftover {
  dataBearing: boolean;
  name: string;
  op: string;
  reason: string;
  subject: string;
}

export interface LicenseInfo {
  commercial: boolean;
  id: string;
  note?: string;
  title: string;
  url: string;
}

export interface Link {
  label: string;
  path: string;
}

export interface LinkedService {
  environment: string;
  message?: string;
  reachable: boolean;
  service: string;
}

export interface LocationInputBody {
  accountId: string;
  checkedAt: string;
  cidrs?: string[];
  cloudRevision: number;
  connectionId: string;
  evidenceRevision: number;
  failureDomains?: {
  dependencyIds?: string[];
  id: string;
  kind: string;
  source: string;
  verified: boolean;
}[];
  id?: string;
  name: string;
  networkRefs?: string[];
  provider: string;
  region: string;
  revision: number;
}

export interface LocationPolicy {
  preferredLocations?: string[];
  preferredProviders?: string[];
  ranking?: string;
  revision: number;
}

export interface LocationPolicyBody {
  preferredLocations?: string[];
  preferredProviders?: string[];
  ranking?: string;
  revision: number;
}

export interface LocationVerificationInputBody {
  cloudRevision: number;
  evidenceRef: string;
  failureDomains: {
  dependencyIds?: string[];
  id: string;
  kind: string;
}[];
  revision: number;
}

export interface Log {
  actionId: string;
  attempt: string;
  deploymentId: string;
  id: string;
  level: string;
  message: string;
  operationId: string;
  orgId: string;
  projectId: string;
  providerId: string;
  source: string;
  stream: string;
  time: string;
  truncated: boolean;
}

export interface LogPage {
  gap: boolean;
  logs: Log[];
  next: string;
}

export interface ManagedDatabaseConfig {
  accountIdentity: string;
  cloudSqlPsc?: CloudSQLSourceEndpoint;
  connectionId: string;
  profile: ManagedDatabaseProfile;
}

export interface ManagedDatabaseProfile {
  access: string;
  availability: string;
  backupRetentionUnit: string;
  backupRetentionValue: number;
  computeClass: string;
  finalBackup: string;
  finalBackupRetentionDays: number;
  id: string;
  kind: string;
  pointInTimeRecoveryDays: number;
  provider: string;
  retainAutomatedBackups: boolean;
  revision: string;
  storageEncrypted: boolean;
  storageGb: number;
  storageType: string;
  version: string;
}

export interface ManagedRecord {
  hostname?: string;
  kind: string;
  name: string;
  projectId?: string;
  projectName?: string;
  routeId?: string;
  ttl: number;
  type: string;
  value: string;
  zoneId: string;
}

export interface ManagedRegistryBackup {
  createdAt: string;
  id: string;
  size: number;
}

export interface ManagedRegistryView {
  action: string;
  backups: ManagedRegistryBackup[];
  connectionId: string;
  dnsReady: boolean;
  generation: number;
  hostname: string;
  message: string;
  phase: string;
  status: string;
}

export interface Manifest {
  databaseGateway?: DatabaseGateway;
  defaults?: Defaults;
  environments?: Record<string, any>;
  federation?: Federation;
  gateways?: Record<string, any>;
  held?: Held;
  ignore?: Ignore;
  links?: Record<string, any>;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
}

export interface ManifestDraft {
  manifest?: Manifest;
  projectId: string;
  sources: SourceRef[];
  yaml: string;
}

export interface ManifestEndpoint {
  hostPort?: number;
  hostname?: string;
  ingressClass?: string;
  tlsSecret?: string;
}

export interface ManifestGPU {
  count: number;
  kind: string;
}

export interface ManifestUse {
  enabled?: boolean;
  external?: string;
  resource?: string;
}

export interface MemberPrerequisite {
  attachmentId: string;
  createdAt: string;
  drainPolicy: DrainPolicy;
  hostnames: string[];
  id: string;
  namespace: string;
  namespaceUid: string;
  orgId: string;
  projectId: string;
  renewalAuthorityGeneration: number;
  renewalAuthorityId: string;
  revision: number;
  schemaVersion: number;
  timeoutVerificationId?: string;
  tlsSecretName: string;
  tlsSecretUid: string;
  updatedAt: string;
}

export interface MemberPrerequisiteInputBody {
  attachmentId: string;
  drainPolicy: {
  maxConnectionLifetimeSeconds?: number;
  mode: string;
  terminationGraceSeconds: number;
};
  expectedRenewalAuthorityGeneration: number;
  expectedRevision: number;
  expectedTlsSecretUid: string;
  id?: string;
  renewalAuthorityId: string;
  tlsSecretName: string;
}

export interface MigrationFile {
  additions: number;
  deletions: number;
  patch: string;
  path: string;
  status: string;
  truncated: boolean;
}

export interface ModelDeploymentList {
  models: (ModelsStatus | null)[];
}

export interface ModelsDeployment {
  accruedAt: string;
  createdAt: string;
  engine: string;
  entryId: string;
  environmentId: string;
  hourlyUsdOverride?: string;
  id: string;
  message?: string;
  month: string;
  monthCostUsd: string;
  monthlyBudgetUsd?: string;
  phase: string;
  planId?: string;
  projectId: string;
  quantization: string;
  resource: string;
  revision: number;
  serverCloudId?: string;
  serverName?: string;
  serverOperationId?: string;
  updatedAt: string;
}

export interface ModelsMoveInputBody {
  maxModelLen?: number;
  quantization?: string;
  reviewServer?: boolean;
  server?: {
  cloudId: string;
  operationId: string;
  review: string;
};
  target: {
  connectionId: string;
  gpu?: GPURequest;
  provider?: string;
  region?: string;
  sizeId?: string;
  source: string;
};
}

export interface ModelsStatus {
  canEdit: boolean;
  connectionId: string;
  connectionName: string;
  cost: Cost;
  deployment?: ModelsDeployment;
  engineVersion: string;
  entry: EntrySummary;
  environments: Reach[];
  gpu?: GPURequest;
  hint?: string;
  internalUrl?: string;
  kind: string;
  linked: LinkedService[];
  maxModelLen: number;
  message?: string;
  monthToDateUsd: string;
  moveCost?: Cost;
  pin?: PinStatus;
  planId?: string;
  projectId: string;
  projectedUsd: string;
  resource: string;
  state: string;
  updateAvailable: boolean;
  verification: Fit;
}

export interface ModelsUpdateInputBody {
  hourlyUsdOverride?: string;
  monthlyBudgetUsd?: string;
  revision: number;
}

export interface MoneyComponent {
  category: string;
  hourlyMicroUsd?: string;
  name: string;
  priceCheckedAt?: string;
  quantity: string;
  source: string;
}

export interface MoneyEstimate {
  checkedAt: string;
  components: MoneyComponent[];
  horizonMicroUsd: string;
  horizonSeconds: number;
  hourlyMicroUsd: string;
  known: boolean;
  missingComponents: string[];
  priceDigest: string;
}

export interface MoveAll {
  failed: string[];
  host: Host;
  moved: number;
  skipped: string[];
}

export interface MoveInput {
  architecture?: string;
  destinationServerId: string;
  environmentId: string;
  poolId: string;
  projectId: string;
  review?: string;
  service: string;
}

export interface MoveReview {
  input: MoveInput;
  placement: Placement;
  review: string;
  sourceServerId: string;
}

export interface NativeCopyCapacity {
  availability: string;
  computeClass: string;
  iops?: number;
  maxStorageGb?: number;
  profileId: string;
  profileRevision: string;
  storageGb: number;
  storageType: string;
  throughputMbps?: number;
}

export interface NativeCopyCreateInputBody {
  acknowledgeCharges: boolean;
  requestId: string;
  reviewToken: string;
}

export interface NativeCopyCredentials {
  checkedAt: string;
  connectionName: string;
  copyId: string;
  database: string;
  expiresAt: string;
  password: string;
  privateServiceConnect?: boolean;
  provider: string;
  username: string;
}

export interface NativeCopyReview {
  capacity: NativeCopyCapacity;
  notice: string;
  pointId: string;
  requestId: string;
  restoreTime?: string;
  reviewToken: string;
  sourceId: string;
  target: NativeRestoreTarget;
  ttlHours: number;
  validUntil: string;
}

export interface NativeCopyReviewInputBody {
  cloudId: string;
  requestId: string;
  restoreTime?: string;
  targetId: string;
  ttlHours: number;
}

export interface NativeInventory {
  nextBefore?: string;
  points: NativePoint[];
  source: NativeSource;
}

export interface NativePITRBounds {
  earliest: string;
  latest: string;
  protocol: number;
  settingsDigest?: string;
  sourceBackup?: string;
  sourceDeletedAt?: string;
}

export interface NativePITRWindow {
  available: boolean;
  earliest?: string;
  latest?: string;
  observedAt?: string;
  pointId?: string;
  provider: string;
  sourceId: string;
  unavailableReason?: string;
}

export interface NativePoint {
  createdAt: string;
  encrypted: boolean;
  engine: string;
  expiresAt: string | null;
  id: string;
  kind: string;
  kmsKey?: string;
  lastSeenAt: string;
  name: string;
  pitr?: NativePITRBounds;
  present: boolean;
  providerId: string;
  restoreTime?: string;
  sourceId: string;
  status: string;
  version: string;
}

export interface NativeRecoveryCopies {
  copies: NativeRestore[];
  nextBefore?: string;
}

export interface NativeRecoverySources {
  nextBefore?: string;
  sources: NativeSource[];
}

export interface NativeRestore {
  cleanupBy?: string;
  cleanupRequestedAt?: string;
  createdAt: string;
  createdBy: string;
  durationMs: number;
  environmentId: string;
  error?: string;
  expiresAt: string;
  externalId?: string;
  id: string;
  orgId: string;
  physicalId?: string;
  pointId: string;
  projectId: string;
  restoreTime?: string;
  sourceId: string;
  status: string;
  target: NativeRestoreTarget;
  verifiedAt?: string;
}

export interface NativeRestoreTarget {
  accountIdentity: string;
  cloudId?: string;
  cloudRevision?: number;
  cloudSqlNetwork?: CloudSQLRecoveryNetwork;
  connectionId: string;
  grantRevision?: number;
  nativeAccount?: string;
  nativeProject?: string;
  provider: string;
  rdsNetwork?: RDSRecoveryNetwork;
  region: string;
  targetId?: string;
  targetRevision?: number;
}

export interface NativeRetentionPolicy {
  authorizedBy: string;
  createdAt: string;
  createdBy: string;
  enabled: boolean;
  id: string;
  lastError?: string;
  lastRetirementId?: string;
  nextAt: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  revision: number;
  sourceId: string;
  updatedAt: string;
}

export interface NativeRetentionPolicyState {
  configured: boolean;
  policy?: NativeRetentionPolicy;
}

export interface NativeRetirement {
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  error?: string;
  id: string;
  orgId: string;
  pointId: string;
  policy: RetentionInput;
  policyId?: string;
  policyRevision?: number;
  projectId: string;
  sourceId: string;
  status: string;
  witnessId: string;
}

export interface NativeRetirementInputBody {
  acknowledgeDeletion: boolean;
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  requestId: string;
}

export interface NativeRetirementPage {
  nextBefore?: string;
  retirements: NativeRetirement[];
}

export interface NativeSource {
  capturedAt: string;
  checkedAt?: string;
  complete: boolean;
  connectionId: string;
  environmentId: string;
  error?: string;
  id: string;
  name: string;
  observedAt?: string;
  orgId: string;
  physicalId: string;
  projectId: string;
  provider: string;
  region: string;
  state: string;
}

export interface Need {
  gpus: number;
  maxModelLen: number;
  perGpuGib: number;
  ramGib?: number;
}

export interface NewHost {
  clientCertificate: string;
  clientFingerprint: string;
  host: Host;
}

export interface NewKeyBody {
  access: string;
  expiresInDays?: number;
  name: string;
}

export interface Node {
  createdAt: string;
  edgeScope: string;
  endpoints: string[];
  id: string;
  identityGeneration: number;
  ingressApplied: string;
  ingressDetail: string;
  ingressMode: string;
  inventory: Inventory;
  joinedBy: string;
  joinedByKeyId: string;
  keyFingerprint: string;
  labels: Record<string, any>;
  lastSeenAt?: string;
  name: string;
  orgId: string;
  protocolVersion: number;
  publicAddress: string;
  publicAddressConfirmedAt?: string;
  revokedAt?: string;
  revokedBy: string;
  role: string;
  status: string;
  updatedAt: string;
  version: string;
}

export interface NodeAdoption {
  approvedBy: string;
  cloudId: string;
  createdAt: string;
  nodeId: string;
  nodeName: string;
  operationId: string;
  orgId: string;
  reason: string;
  serverId: string;
  status: string;
  updatedAt: string;
}

export interface NodeAdoptionList {
  adoptions: (NodeAdoption | null)[];
}

export interface NodeJoinBody {
  endpoints?: string[];
  inventory: {
  arch: string;
  cpus: number;
  diskGib: number;
  docker?: Runtime;
  endpoints?: string[];
  gpus?: NodeprotoGPU[];
  hostname?: string;
  kernel: string;
  memoryMib: number;
  os: string;
  podman?: Runtime;
  ports80443?: string;
  publicAddresses?: string[];
};
  labels?: Record<string, any>;
  name?: string;
  nodeVersion: string;
  protocolVersion: number;
  publicKey: string;
  signature: string;
  token: string;
}

export interface NodeJoined {
  cloudId?: string;
  endpoints: string[];
  identityGeneration: number;
  name?: string;
  nodeId: string;
  orgId: string;
}

export interface NodeList {
  nodes: Node[];
}

export interface NodePoolSpec {
  autoscale: boolean;
  count: number;
  id?: string;
  labels?: Record<string, any>;
  max: number;
  min: number;
  name: string;
  providerOptions?: ClusterProviderOptions;
  size: string;
  taints?: NodePoolTaint[];
}

export interface NodePoolTaint {
  effect: string;
  key: string;
  value: string;
}

export interface NodeUpdateBody {
  endpoints?: string[];
  ingressMode?: string;
  labels?: Record<string, any>;
  name?: string;
}

export interface NodeWorkload {
  bindingId: string;
  environment: string;
  hasBackupPolicy: boolean;
  kind: string;
  projectId: string;
  service: string;
}

export interface NodeWorkloadList {
  workloads: NodeWorkload[];
}

export interface NodeprotoGPU {
  memoryMib: number;
  model: string;
  vendor: string;
}

export interface Note {
  message: string;
  path?: string;
  source?: string;
}

export interface Notification {
  actionUrl: string;
  body: string;
  createdAt: string;
  emailStatus: string;
  id: string;
  projectId: string;
  read: boolean;
  status: string;
  title: string;
}

export interface NotificationCreated {
  id: string;
  orgId: string;
}

export interface NotificationList {
  items: Notification[];
  mailEnabled: boolean;
}

export interface OK {
  ok: boolean;
}

export interface OctopusRouteInputBody {
  hostname: string;
  methods: string[];
  note?: string;
  path: string;
  port: number;
  revision?: number;
  service: string;
  stripPrefix: boolean;
}

export interface OctopusStatus {
  appliedGeneration: number;
  attach: AttachDefaults;
  farpHostnames: string[];
  generation: number;
  namespace?: string;
  privateUrl?: string;
  provider: string;
  resource: string;
  revision: number;
  startTime?: string;
  syncError?: string;
  syncedAt: string;
  version?: string;
}

export interface OctopusloomRoute {
  createdAt: string;
  createdBy: string;
  environmentId: string;
  hostname: string;
  id: string;
  methods: string[];
  note?: string;
  orgId: string;
  path: string;
  port: number;
  projectId: string;
  resource: string;
  revision: number;
  service: string;
  stripPrefix: boolean;
  updatedAt: string;
}

export interface OpenCompatModel {
  contextWindow?: number;
  dimensions?: number;
  embeddingPerMillion?: string;
  id: string;
  inputPerMillion: string;
  maxOutput?: number;
  outputPerMillion: string;
  task: string;
}

export interface OpenReport {
  id: string;
  openedAt: string;
  path: string;
  severity?: string;
  title: string;
}

export interface Operation {
  absenceConfirmedAt?: string;
  accelerator?: Accelerator;
  accountId: string;
  actions: InfrastructureAction[];
  attempt: number;
  attempted: boolean;
  bootstrapExpiresAt: string;
  budgetReview?: BudgetReview;
  capacityCheckedAt?: string;
  capacityTarget?: ResourceVector;
  cloudId: string;
  connectionGeneration?: number;
  connectionId: string;
  connectionRevision: string;
  createdAt: string;
  enrolled: boolean;
  estimatedHourlyUsd: number;
  expiresAt: string;
  hostKey?: string;
  hourlyPriceKnown?: boolean;
  id: string;
  input: ServerInput;
  message: string;
  moneyEstimate?: MoneyEstimate;
  orgId: string;
  ownership: string;
  provider: string;
  publicIpv4: string[];
  resourceId: string;
  review: string;
  serverId: string;
  stateRevision: number;
  status: string;
  updatedAt: string;
}

export interface OperationList {
  operations: (Operation | null)[];
}

export interface OperationResult {
  instance: Instance;
  operation: FleetOperation;
}

export interface OrgInvitation {
  createdAt: string;
  email: string;
  expiresAt: string;
  id: string;
  invitedBy: string;
  role: string;
}

export interface OrgInvitationList {
  invitations: OrgInvitation[];
}

export interface OrgMember {
  email: string;
  id: string;
  joinedAt: string;
  memberRole: string;
  name: string;
  role: string;
  userId: string;
}

export interface OrgMemberList {
  members: OrgMember[];
}

export interface Output {
  envKey: string;
  from?: Ref;
  name: string;
  perConsumer?: boolean;
  publicCa?: string;
  requires?: string;
  secret?: boolean;
  template?: string;
}

export interface OutputInfo {
  envKey: string;
  name: string;
  perConsumer: boolean;
}

export interface Package {
  ecosystem: string;
  evidence: DiscoveryEvidence[];
  id: string;
  name: string;
  path: string;
  requires?: Requirement[];
  runtime?: string;
  source?: string;
}

export interface PageContext {
  environment?: string;
  kind?: string;
  label: string;
  path: string;
  planId?: string;
  projectId?: string;
  service?: string;
}

export interface PendingNodeJoinToken {
  cloudId?: string;
  createdAt: string;
  edge: boolean;
  edgeScope?: string;
  endpoints: string[];
  expiresAt: string;
  hint: string;
  id: string;
  labels?: Record<string, any>;
  mintedBy: string;
  mintedByKeyId?: string;
}

export interface PinStatus {
  file?: string;
  message?: string;
  model?: string;
  revision?: string;
  updateAvailable: boolean;
}

export interface Placement {
  allocation?: string;
  connectionGeneration: number;
  connectionId: string;
  cpuMillis: number;
  gpuKind?: string;
  gpus?: number;
  memoryBytes: number;
  poolId: string;
  poolRevision: number;
  serverId: string;
  serverRevision: number;
}

export interface PlacementGroupMemberReview {
  memberId: string;
  replicas: number;
}

export interface PlacementGroupReview {
  generation: number;
  groupId: string;
  hostname: string;
  image: string;
  members: PlacementGroupMemberReview[];
  minReady: number;
  name: string;
  revision: string;
  routingMode: string;
  service: string;
}

export interface PlacementUsage {
  default?: boolean;
  services: string[];
  targetId?: string;
}

export interface Plan {
  actions: Action[];
  artifacts: ArtifactChoice[];
  catalog: boolean;
  cloudSyncId?: string;
  connections: Record<string, any>;
  createdAt: string;
  createdBy: string;
  decidedAt?: string;
  decidedBy: string;
  decisionNote: string;
  environment: string;
  environmentId: string;
  error: string;
  executable: boolean;
  expiresAt?: string;
  fromEnvironment: string;
  fromEnvironmentId: string;
  id: string;
  kind: string;
  leftovers: Leftover[];
  manifestId: string;
  manifestRevision: number;
  orgId: string;
  placementGroups?: PlacementGroupReview[];
  plannedAt?: string;
  problems: Finding[];
  projectId: string;
  release?: Release;
  revision: string;
  rollback?: Rollback;
  services?: string[];
  sources: PlanSource[];
  status: string;
  summary: PlanningSummary;
  warnings: Finding[];
}

export interface PlanCounts {
  approved: number;
  expired: number;
  failed: number;
  rejected: number;
  superseded: number;
  waiting: number;
}

export interface PlanList {
  next?: string;
  plans: Plan[];
}

export interface PlanSource {
  alias: string;
  analysisId: string;
  commitSha: string;
  fromPlanId?: string;
  inScope: boolean;
  ref?: string;
  repository: string;
  root: string;
  sourceId: string;
}

export interface PlanSummary {
  adopt: number;
  create: number;
  delete: number;
  keep: number;
  replace: number;
  update: number;
}

export interface PlanningCloudList {
  clouds: (Profile | null)[];
}

export interface PlanningSummary {
  create: number;
  delete: number;
  keep: number;
  replace: number;
  update: number;
}

export interface PlanningTenantInputBody {
  tenantId: string;
}

export interface PlanningTenantList {
  tenants: FabriqloomTenant[];
}

export interface PlatformDNSSettings {
  provider: string;
  zone: string;
}

export interface PlatformLimits {
  concurrentBuilds: number;
  memoryMib: number;
  vcpus: number;
  vms: number;
}

export interface PlatformcloudEvidence {
  architecture: string;
  checkedAt: string;
  deniedPorts: number[];
  ingressAddress: string;
  isolation: boolean;
  perOrgMemoryMib: number;
  perOrgVcpus: number;
  perOrgVms: number;
  platform: boolean;
  protocolVersion: string;
  reason?: string;
  runtimeIdentity: string;
  smtActive: boolean;
}

export interface PlatformcloudTenant {
  limits?: PlatformLimits;
  orgId: string;
  revision: number;
  status: string;
  suspendReasons: string[];
  suspendedAt?: string;
}

export interface PlatformcloudTenantInputBody {
  limits?: {
  concurrentBuilds: number;
  memoryMib: number;
  vcpus: number;
  vms: number;
};
  status: string;
}

export interface PlatformcloudTenantList {
  tenants: TenantRow[];
}

export interface PlatformcloudUse {
  concurrentBuilds: number;
  memoryMib: number;
  vcpus: number;
  vms: number;
}

export interface Point {
  t: string;
  v: number;
}

export interface Policy {
  createdBy: string;
  enabled: boolean;
  environment: string;
  environmentId: string;
  everyHours: number;
  id: string;
  lastError?: string;
  nextAt: string;
  orgId: string;
  projectId: string;
  resource: string;
}

export interface PolicyInputBody {
  enabled: boolean;
  environment: string;
  everyHours: number;
  resource: string;
}

export interface PolicyList {
  policies: Policy[];
}

export interface Pool {
  acceleratorKind?: string;
  cloudId: string;
  id: string;
  members: PoolMember[];
  name: string;
  orgId: string;
  revision: number;
}

export interface PoolExpansionPolicy {
  acceleratorKind?: string;
  allowedSizes: string[];
  architecture: string;
  bootstrapSize: string;
  connectionId: string;
  image: string;
  limits?: ResourceVector;
  maxServers: number;
  minServers: number;
  poolId: string;
  region: string;
  systemReserve: ResourceVector;
}

export interface PoolInputBody {
  acceleratorKind?: string;
  members: {
  cpuMillis: number;
  draining: boolean;
  gpus?: number;
  memoryBytes: number;
  serverId: string;
}[];
  name: string;
  revision: number;
}

export interface PoolList {
  pools: (Pool | null)[];
}

export interface PoolMember {
  cpuMillis: number;
  draining: boolean;
  gpus?: number;
  memoryBytes: number;
  serverId: string;
}

export interface Port {
  container: number;
  control?: boolean;
  name: string;
  protocol: string;
  routable?: boolean;
}

export interface PreferenceInputBody {
  agentEmail?: boolean;
  deploymentEmail?: boolean;
}

export interface Preferences {
  agentEmail: boolean;
  deploymentEmail: boolean;
  mailEnabled: boolean;
}

export interface PrepareInputBody {
  from?: string;
  replace?: boolean;
  scope?: string[];
  to?: string;
}

export interface Preview {
  after?: CloudprofilesTarget;
  afterAssignment?: Assignment;
  afterDefault?: DefaultPlacement;
  before?: CloudprofilesTarget;
  beforeAssignment?: Assignment;
  beforeDefault?: DefaultPlacement;
  blockers: string[];
  createdAt: string;
  environments: EnvironmentChange[];
  id: string;
  input: ChangeInput;
  orgId: string;
  projectId: string;
  reverts?: string;
  revision: string;
}

export interface Problem {
  code: string;
  message: string;
}

export interface Profile {
  createdAt: string;
  defaultPlacement: DefaultPlacement | null;
  id: string;
  name: string;
  orgId: string;
  platformOwned: boolean;
  projectId: string;
  revision: number;
  updatedAt: string;
}

export interface Project {
  autonomousPromotion: boolean;
  color: string;
  createdAt: string;
  createdBy: string;
  deploymentMode: string;
  description: string;
  exclusions: Exclusion[];
  icon: string;
  id: string;
  minStagingSoakSeconds: number;
  name: string;
  orgId: string;
  repositories: string[];
  responsibleTeamId: string;
  slug: string;
  status: string;
  updatedAt: string;
}

export interface ProjectDomain {
  hostnames: HostnameUse[];
  mode: string;
  name: string;
  provider?: string;
  verified: boolean;
  wildcards: boolean;
  zoneId: string;
}

export interface ProjectDomainList {
  domains: ProjectDomain[];
}

export interface ProjectGrant {
  cloudId: string;
  orgId: string;
  projectId: string;
  revision: number;
  revoked: boolean;
  targetIds: string[];
}

export interface ProjectList {
  projects: Project[];
}

export interface ProjectPermissions {
  permissions: string[];
}

export interface ProjectView {
  excluded: Excluded[];
  graph?: Graph;
  projectId: string;
  sources: SourceState[];
}

export interface ProjectZone {
  id: string;
  mode: string;
  name: string;
  verified: boolean;
}

export interface ProjectZoneList {
  zones: ProjectZone[];
}

export interface Provider {
  available: boolean;
  buildLogs?: string;
  cloud: string;
  description: string;
  infrastructure?: string[];
  logNote?: string;
  model: string;
  name: string;
  note?: string;
  provisions?: Provisions[];
  regions?: string[];
  runs?: Runs[];
  scaling: boolean;
  title: string;
  workloads?: WorkloadCapabilities;
}

export interface ProviderEnvironment {
  createdAt: string;
  environmentId: string;
  mode: string;
  permanent: boolean;
  provider: string;
  reason: string;
  scope: string;
}

export interface ProviderEnvironmentList {
  providerEnvironments: ProviderEnvironment[];
}

export interface ProviderKey {
  createdAt: string;
  createdBy: string;
  fields?: Record<string, any>;
  id: string;
  keyHint?: string;
  models?: OpenCompatModel[];
  name: string;
  orgId: string;
  projects: string[];
  revision: number;
  type: string;
  updatedAt: string;
  updatedBy: string;
}

export interface ProviderKeyInputBody {
  fields?: Record<string, any>;
  models?: {
  contextWindow?: number;
  dimensions?: number;
  embeddingPerMillion?: string;
  id: string;
  inputPerMillion: string;
  maxOutput?: number;
  outputPerMillion: string;
  task: string;
}[];
  projects?: string[];
  type: string;
}

export interface ProviderList {
  providers: Provider[];
}

export interface ProviderResource {
  accountIdentity: string;
  apiAccountId?: string;
  attachmentNote?: string;
  bucketOwner?: string;
  connectionGeneration: number;
  connectionId: string;
  connectionIdentity: string;
  connectionState: string;
  consumers: ProviderResourceConsumer[];
  credentialMethods: string[];
  database: string;
  endpointId: string;
  endpointProofRequired: boolean;
  endpointRevision: number;
  externalId: string;
  host: string;
  id: string;
  identityScope: string;
  incarnation: string;
  kind: string;
  name: string;
  observationState: string;
  observedAt: string;
  orgId: string;
  ownership: string;
  ownershipState: string;
  port: number;
  provider: string;
  region: string;
  revision: number;
  scope: string;
  status: string;
  version: string;
}

export interface ProviderResourceConnectInputBody {
  apiAccess?: {
  accountId?: string;
  className?: string;
  environment?: string;
  incarnation?: string;
  jurisdiction?: string;
  kind?: string;
  name?: string;
  native?: boolean;
  projectId?: string;
  provider?: string;
  resourceId?: string;
  scriptName?: string;
  token?: string;
  values?: Record<string, any>;
};
  bucketAccess?: {
  accessKeyId?: string;
  accountId?: string;
  bucket?: string;
  expectedOwner?: string;
  jurisdiction?: string;
  provider?: string;
  region?: string;
  secretAccessKey?: string;
  serviceAccountJson?: string;
  sessionToken?: string;
};
  dsn?: string;
  providerEnvironment?: string;
  providerProjectId?: string;
}

export interface ProviderResourceConsumer {
  bindingId: string;
  environmentId: string;
  id: string;
  name: string;
  projectId: string;
}

export interface ProviderResourceList {
  resources: (ProviderResource | null)[];
  supportedProviders: string[];
  syncs: (ProviderResourceSync | null)[];
}

export interface ProviderResourceSync {
  accountIdentity: string;
  connectionGeneration: number;
  connectionId: string;
  connectionIdentity: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  provider: string;
  refreshOptIn: boolean;
  requestedBy: string;
  requestedKeyId?: string;
  scope: string;
  sequence: number;
  startedAt: string;
  status: string;
}

export interface ProviderStatus {
  capabilities: string[];
  name: string;
  region: string;
  version: string;
}

export interface ProviderZoneList {
  zones: DNSZone[];
}

export interface Provisions {
  creates: string;
  importOnly?: boolean;
  kind: string;
  managedProfiles?: ManagedDatabaseProfile[];
  settings?: ResourceSetting[];
  versions?: string[];
}

export interface PublicConfig {
  appId: string;
  authBasePath: string;
  cloudSignInOffer?: boolean;
  consolePath?: string;
  features: Features;
  platformAdmin?: boolean;
  publishableKey: string;
  requireEmailVerification: boolean;
  setupPending: boolean;
  setupTokenRequired: boolean;
  waitlist: boolean;
}

export interface PushConfig {
  enabled: boolean;
  gatewayUrl: string;
  keyId: string;
  serverKey: string;
}

export interface PushDevice {
  createdAt: string;
  id: string;
  lastSeenAt: string;
  name: string;
  platform: string;
  quietHours?: QuietHours;
  settings: Record<string, any>;
  timeZone: string;
}

export interface PushState {
  on: boolean;
  reason?: string;
}

export interface QuantizationView {
  args?: string[];
  engine: string;
  file?: string;
  fits: Fit[];
  id: string;
  minGpuMemoryGib?: number;
  minMemoryGib?: number;
  repo?: string;
  revision?: string;
  weightBytes: number;
}

export interface QuietHours {
  end: string;
  start: string;
}

export interface RDSRecoveryNetwork {
  securityGroupId: string;
  subnetGroup: string;
  verifierGroupId: string;
  vpcId: string;
}

export interface Reach {
  environment: string;
  environmentId: string;
  message?: string;
  mode: string;
}

export interface ReadinessEvidence {
  artifacts: ArtifactChoice[];
  backupSchedule: boolean;
  budget?: BudgetState;
  canaries: CanarySummary[];
  changedServices: ChangedService[];
  collectedAt: string;
  coverage?: Coverage;
  dataUpdates: string[];
  destroy: string[];
  openReports: OpenReport[];
  pinDifferences: string[];
  pinsMatch: boolean;
  planStatus: string;
  problems: Finding[];
  productionRelease?: ReleaseEvidence;
  soak?: Soak;
  sources: SourceDiff[];
  stagingRelease?: ReleaseEvidence;
  unavailable: Record<string, any>;
  warnings: Finding[];
}

export interface RealtimeAgentEnd {
  error?: string;
  status: string;
}

export interface RealtimeAgentEvent {
  at: string;
  data: string;
  kind: string;
  seq: number;
  taskId: string;
}

export interface RealtimeForgeGap {
  reason: string;
}

export interface RealtimeForgeResumed {
  count: number;
  from: string;
}

export interface RealtimeRealtimeSnapshot {
  cursor: string;
  partial: boolean;
  records: SnapshotRecord[];
}

export interface RealtimeRealtimeStatus {
  cursor: string;
  time: string;
}

export interface RealtimeReceive {
  channel: string;
  cursor: string;
  data: any;
  event: string;
}

export interface RealtimeSend {
  cursor?: string;
  deploymentIds?: string[];
  op: string;
  orgId: string;
  projectIds?: string[];
}

export interface Rebuild {
  createdAt: string;
  createdBy: string;
  finishedAt?: string;
  id: string;
  output: string;
  projection: string;
  retryable: boolean;
  status: string;
}

export interface RebuildInputBody {
  projection: string;
  requestId: string;
}

export interface RebuildList {
  rebuilds: Rebuild[];
}

export interface Recipe {
  architecture: string;
  backupFormat: string;
  extensions?: string[];
  family?: string;
  fsGroup?: number;
  id: string;
  image: string;
  mountPath: string;
  productVersion: string;
  revision: string;
  volumeSizeMb?: number;
}

export interface RecipesGPU {
  count: number;
  kind?: string;
  minMemoryGib?: number;
}

export interface RecoveryInputBody {
  planId: string;
  review?: string;
  service: string;
}

export interface Ref {
  component?: string;
  dependency?: string;
  field: string;
}

export interface Region {
  displayName: string;
  id: string;
  name: string;
  revision: number;
  status: string;
}

export interface RegionInputBody {
  displayName: string;
  name: string;
  status?: string;
}

export interface RegionList {
  regions: Region[];
}

export interface RegionUpdateBody {
  displayName?: string;
  revision?: number;
  status?: string;
}

export interface RegisterDeviceInputBody {
  handle: string;
  name: string;
  platform: string;
  publicKey: string;
  settings: Record<string, any>;
  timeZone: string;
}

export interface Registration {
  accountId: string;
  attempt: number;
  attempted: boolean;
  autoRenew: boolean;
  connectionId: string;
  createdAt: string;
  customRegistrant: boolean;
  domainExpiresAt: string;
  firstUnconfirmedAt: string;
  id: string;
  message: string;
  name: string;
  noticeSentFor: string;
  orgId: string;
  privacyMode: string;
  projectIds: string[];
  provider: string;
  quote: DomainPrice;
  refreshedAt: string;
  resultNotifiedFor: string;
  review: string;
  reviewExpiresAt: string;
  revision: number;
  sandbox: boolean;
  status: string;
  updatedAt: string;
  zoneId: string;
}

export interface RegistrationApprovalBody {
  currency: string;
  registrationCost: string;
  review: string;
}

export interface RegistrationExtensionList {
  extensions: string[];
}

export interface RegistrationInputBody {
  autoRenew?: boolean;
  connectionId: string;
  name: string;
  privacyMode?: string;
  projectIds?: string[];
  registrant?: {
  city: string;
  countryCode: string;
  email: string;
  name: string;
  organization: string;
  phone: string;
  postalCode: string;
  state: string;
  street: string;
};
}

export interface RegistrationList {
  registrations: (Registration | null)[];
}

export interface RegistryActionInputBody {
  backupId?: string;
  deleteData?: boolean;
  generation: number;
  hostname: string;
  kind: string;
  removeSetup?: boolean;
}

export interface RegistryList {
  counts: Record<string, any>;
  entries: EntrySummary[];
  fitsNotice?: string;
}

export interface RegistrySettings {
  authEndpoint: string;
  endpoint: string;
  repository: string;
}

export interface Release {
  canary?: string[];
  holdSeconds?: number;
  steps?: number[];
  strategy: string;
  thresholds: Thresholds;
}

export interface ReleaseEvidence {
  deploymentId: string;
  environment: string;
  reason?: string;
  settled: boolean;
  status?: string;
  unverified: boolean;
  verdict?: string;
  watchId?: string;
  watched: boolean;
}

export interface ReleaseRef {
  deploymentId: string;
  finishedAt: string;
  kind: string;
  planId: string;
  status: string;
}

export interface ReleaseReport {
  concerns: Concern[];
  createdAt: string;
  decidedBy: string;
  decision: string;
  error: string;
  evaluatedAt?: string;
  evidence?: ReadinessEvidence;
  finishedAt?: string;
  fromEnvironment: string;
  fromEnvironmentId: string;
  gates: GateCheck[];
  id: string;
  orgId: string;
  planId: string;
  planRevision: string;
  preview: boolean;
  projectId: string;
  requestId: string;
  requestedBy: string;
  reusedPlan: boolean;
  runId: string;
  scope: string[];
  state: string;
  stateReason: string;
  status: string;
  taskId: string;
  toEnvironment: string;
  toEnvironmentId: string;
  updatedAt: string;
  via: string;
}

export interface ReleaseReportEvent {
  createdAt: string;
  decision: string;
  error: string;
  evaluatedAt?: string;
  finishedAt?: string;
  fromEnvironment: string;
  fromEnvironmentId: string;
  gates: GateCheck[];
  id: string;
  orgId: string;
  planId: string;
  planRevision: string;
  preview: boolean;
  projectId: string;
  requestedBy: string;
  reusedPlan: boolean;
  runId: string;
  scope: string[];
  state: string;
  stateReason: string;
  status: string;
  taskId: string;
  toEnvironment: string;
  toEnvironmentId: string;
  updatedAt: string;
  via: string;
}

export interface ReleaseReportList {
  reports: (ReleaseReport | null)[];
}

export interface RemoveRouteInputBody {
  revision: number;
}

export interface ReplaceInputBody {
  credential: string;
  settings?: Record<string, any>;
}

export interface Report {
  closedBy: string;
  component: string;
  createdAt: string;
  environment: string;
  environmentId: string;
  evidence: AgentreportsEvidence;
  facts: Fact[];
  findings?: Findings;
  fingerprint: string;
  firstSeenAt: string;
  id: string;
  investigation: string;
  investigationNote: string;
  issue: Issue;
  lastSeenAt: string;
  occurrences: number;
  openedBy: string;
  orgId: string;
  projectId: string;
  repository: string;
  resolution: string;
  resolvedAt?: string;
  revision: number;
  severity: string;
  signal: string;
  status: string;
  takenAt?: string;
  takenBy: string;
  taskId: string;
  title: string;
  updatedAt: string;
}

export interface ReportList {
  reports: (Report | null)[];
}

export interface Request {
  answer: string;
  answeredAt?: string;
  answeredBy: string;
  askedFor: string;
  context: Link[];
  createdAt: string;
  environmentId: string;
  expiresAt: string;
  id: string;
  kind: string;
  options: string[];
  orgId: string;
  outcome: string;
  projectId: string;
  question: string;
  routeNote: string;
  routeSource: string;
  runId: string;
  status: string;
  target: AgentrequestsTarget;
  taskId: string;
  teamId: string;
  teamName: string;
  teamReason: string;
  updatedAt: string;
}

export interface RequestEvent {
  askedFor: string;
  createdAt: string;
  id: string;
  kind: string;
  orgId: string;
  projectId: string;
  routeSource: string;
  status: string;
  teamId: string;
  teamName: string;
  updatedAt: string;
}

export interface RequestList {
  requests: (Request | null)[];
}

export interface Requirement {
  evidence: DiscoveryEvidence;
  git?: string;
  name: string;
  version?: string;
}

export interface ReservationRecovery {
  active: boolean;
  phase: string;
  planId: string;
  review: string;
  revision: number;
  serverId: string;
  service: string;
  started: boolean;
  storageRetained: boolean;
}

export interface Resolution {
  environment: string;
  projectId: string;
  service?: string;
  variables: ResolvedVariable[];
}

export interface ResolvedComponent {
  after?: string[];
  args?: string[];
  command?: string[];
  env?: EnvVar[];
  files?: File[];
  image: string;
  kubernetesRules?: KubernetesRule[];
  name: string;
  ports?: Port[];
  protection?: string;
  readiness?: HTTPProbe;
  requires?: string;
  resources: Resources;
  role: string;
  startTimeout?: number;
  step: number;
  timeout?: number;
  volume?: Volume;
  weightCache?: WeightCache;
}

export interface ResolvedVariable {
  key: string;
  overrides: Variable[];
  variable: Variable;
}

export interface Resource {
  confidence: string;
  evidence: DiscoveryEvidence[];
  id: string;
  kind: string;
  needsConfirmation: boolean;
}

export interface ResourceConfig {
  accountIdentity?: string;
  catalog?: Recipe;
  cloudPlacement?: CloudPlacement;
  component?: ComponentConfig;
  composition?: CompositionConfig;
  endpointPins?: Record<string, any>;
  existingResource?: ExistingResourceConfig;
  kind: string;
  managed?: ManagedDatabaseConfig;
  provider: string;
  region?: string;
  routablePorts?: number[];
  settings?: Record<string, any>;
  target?: string;
  targetSettings?: Record<string, any>;
  version?: string;
}

export interface ResourceSetting {
  default?: string;
  name: string;
  pattern: string;
  required?: boolean;
  title: string;
}

export interface ResourceStep {
  cpuMillis?: number;
  memoryMb?: number;
  shmMb?: number;
}

export interface ResourceVector {
  cpuMillis: number;
  gpuKind?: string;
  gpus?: number;
  memoryBytes: number;
  servers: number;
  storageBytes: number;
}

export interface Resources {
  cpuMillis: number;
  gpu?: RecipesGPU;
  memoryMb: number;
  perGpu?: ResourceStep;
  shmMb?: number;
}

export interface Restore {
  backupId: string;
  createdAt: string;
  createdBy: string;
  durationMs: number;
  error?: string;
  expiresAt: string;
  externalId?: string;
  id: string;
  orgId: string;
  projectId: string;
  status: string;
  verifiedAt?: string;
}

export interface RestoreInputBody {
  requestId: string;
}

export interface RestoreList {
  restores: Restore[];
}

export interface Result {
  detail: string;
  engine: string;
  engineVersion: string;
  entry: string;
  gpuKind: string;
  gpus: number;
  maxModelLen: number;
  outcome: string;
  quantization: string;
  ranAt: string;
  revision: string;
  run: string;
}

export interface RetainedPoolStorage {
  environmentId: string;
  planId: string;
  projectId: string;
  service: string;
}

export interface RetainedResource {
  estimatedHourlyMicroUsd?: string;
  id: string;
  kind: string;
  ownership: string;
  survivesClusterDeletion: boolean;
}

export interface RetentionInput {
  keepLast: number;
  olderThanDays: number;
}

export interface RetentionPoint {
  backupId: string;
  bytes: number;
  createdAt: string;
  deletionSupported: boolean;
  deletionUnavailableReason?: string;
  eligible: boolean;
  reason: string;
}

export interface RetentionPolicy {
  anchorId: string;
  authorizedBy: string;
  createdAt: string;
  createdBy: string;
  enabled: boolean;
  id: string;
  lastError?: string;
  lastRetirementId?: string;
  nextAt: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  revision: number;
  updatedAt: string;
}

export interface RetentionPolicyHistory {
  events: RetentionPolicyRevision[];
  nextBefore?: string;
}

export interface RetentionPolicyInputBody {
  enabled: boolean;
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  revision: number;
}

export interface RetentionPolicyRevision {
  authorizedBy: string;
  createdAt: string;
  enabled: boolean;
  policy: RetentionInput;
  revision: number;
}

export interface RetentionPolicyState {
  configured: boolean;
  policy?: RetentionPolicy;
}

export interface RetentionPreview {
  asOf: string;
  complete: boolean;
  eligibleBytes: number;
  eligibleCount: number;
  points: RetentionPoint[];
  verifiedBackupId?: string;
}

export interface Retirement {
  attempt: number;
  attemptLimit: number;
  backupId: string;
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  error?: string;
  executionBy: string;
  id: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  retentionPolicyId?: string;
  retentionRevision?: number;
  status: string;
  witnessId: string;
}

export interface RetirementEvent {
  actor: string;
  attempt: number;
  attemptLimit: number;
  createdAt: string;
  cursor: string;
  error?: string;
  kind: string;
  previousActor?: string;
  previousError?: string;
  status: string;
}

export interface RetirementHistory {
  events: RetirementEvent[];
  nextBefore?: string;
}

export interface RetirementInputBody {
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  requestId: string;
}

export interface RetirementPage {
  nextBefore?: string;
  retirements: Retirement[];
}

export interface RetirementRetryInputBody {
  requestId: string;
}

export interface ReusedArtifact {
  architecture?: string;
  commitSha: string;
  digest?: string;
  fromDeploymentId: string;
  fromEnvironment: string;
  reference?: string;
  revision?: string;
}

export interface Revision {
  createdAt: string;
  createdBy: string;
  digest: string;
  id: string;
  manifest?: Manifest;
  orgId: string;
  projectId: string;
  revision: number;
  yaml: string;
}

export interface RevisionList {
  revisions: Revision[];
}

export interface Rollback {
  reason: string;
  restoreDeploymentId: string;
  restoreFinishedAt: string;
  restorePlanId: string;
  targetDeploymentId: string;
  targetPlanId: string;
  trigger: string;
  watchId?: string;
}

export interface RollbackCommit {
  alias: string;
  from: string;
  to: string;
}

export interface RollbackInputBody {
  deploymentId?: string;
  environment: string;
  reason: string;
}

export interface RollbackTarget {
  commits: RollbackCommit[];
  environment: string;
  restore: ReleaseRef;
  target: ReleaseRef;
}

export interface RouteBinding {
  address: string;
  backend?: string;
  bindingId: string;
  configDigest: string;
  connectionGeneration: number;
  connectionId: string;
  containerId: string;
  externalId?: string;
  ingressClass?: string;
  instanceId: string;
  namespace?: string;
  provider?: string;
  releaseId: string;
  runtimeId: string;
  serverId: string;
  serviceName?: string;
  targetId: string;
}

export interface RouteCost {
  estimate: boolean;
  monthlyUsd: string;
  source: string;
}

export interface RouteInput {
  blockedPathPrefixes?: string[];
  cloudId: string;
  environmentId: string;
  externalTtlSeconds?: number;
  hostname: string;
  port: number;
  service: string;
  zoneId: string;
}

export interface RouteInputBody {
  blockedPathPrefixes?: string[];
  cloudId: string;
  environmentId: string;
  externalTtlSeconds?: number;
  hostname: string;
  port: number;
  service: string;
  zoneId: string;
}

export interface RouteOperation {
  attempt: number;
  automatic?: boolean;
  cost?: RouteCost;
  createdAt: string;
  desiredRecord: DNSRecord;
  desiredRecords?: DNSRecord[];
  dnsAttempted: boolean;
  expiresAt: string;
  generation: number;
  handoffId?: string;
  id: string;
  kind: string;
  message: string;
  notBefore: string;
  notices?: string[];
  previousAddress?: string;
  previousReceipt?: DNSRecord;
  receipt?: DNSRecord;
  receipts?: DNSRecord[];
  review: string;
  revision: number;
  route: CloudinfraRoute;
  source?: CloudinfraRoute;
  status: string;
}

export interface RouteResult {
  route?: OctopusloomRoute;
  sync: SyncResult;
}

export interface RouteRevisionInputBody {
  revision: number;
}

export interface RouteSummary {
  gatewayId: string;
  id: string;
  resource: string;
  state: string;
}

export interface RouteTTLInputBody {
  externalTtlSeconds: number;
  revision: number;
}

export interface RouteVerification {
  backend: boolean;
  backendError?: string;
  certExpires: string;
  certState?: string;
  checkedAt: string;
  dns: boolean;
  dnsError?: string;
  tls: boolean;
  tlsError?: string;
}

export interface RunningExec {
  container: string;
  exec: string;
  name: string;
}

export interface Runs {
  creates: string;
  execution: string;
  workload: string;
}

export interface Runtime {
  socket: string;
  version: string;
}

export interface RuntimeAccessProfile {
  cleanupDependents: boolean;
  hpa: boolean;
  lifecycleJobs?: boolean;
  schemaVersion: number;
}

export interface RuntimeAccessReview {
  attachmentId: string;
  attachmentRevision?: number;
  capabilityRevision?: number;
  namespace?: string;
  profile: RuntimeAccessProfile;
  projectId?: string;
}

export interface RuntimeAttachmentPage {
  clusterId: string;
  items: RuntimeAttachmentSummary[];
}

export interface RuntimeAttachmentSummary {
  capabilityRevision: number;
  id: string;
  namespace: string;
  profile?: RuntimeAccessProfile;
  projectId: string;
  revision: number;
  state: string;
  targetId?: string;
}

export interface RuntimeHealth {
  architecture: string;
  checkedAt: string;
  cpus: number;
  dockerId: string;
  effectiveCpuMillis: number;
  effectiveMemoryBytes: number;
  freeStorageBytes?: number;
  gpuMessage?: string;
  gpuRuntime?: string;
  gpus?: GPUDevice[];
  memoryBytes: number;
  storageBytes?: number;
}

export interface SaveInputBody {
  baseRevision?: number;
  manifest?: {
  databaseGateway?: DatabaseGateway;
  defaults?: Defaults;
  environments?: Record<string, any>;
  federation?: Federation;
  gateways?: Record<string, any>;
  held?: Held;
  ignore?: Ignore;
  links?: Record<string, any>;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
};
  yaml?: string;
}

export interface Scale {
  max: number;
  min: number;
}

export interface ScalingPool {
  policy?: CapacityPolicy;
  pool?: Pool;
}

export interface ScalingPoolInputBody {
  acceleratorKind?: string;
  expectedRevision: number;
  name: string;
  policy: {
  budget?: CapacityBudget;
  cloudId: string;
  enabled: boolean;
  frontendLimits: FrontendLimit[];
  gpuLimits?: GPULimit[];
  limits: ResourceVector;
  orgId: string;
  pools: PoolExpansionPolicy[];
  requireExpansionApproval: boolean;
  revision: number;
};
  template: {
  acceleratorKind?: string;
  allowedSizes: string[];
  architecture: string;
  bootstrapSize: string;
  connectionId: string;
  image: string;
  limits?: ResourceVector;
  maxServers: number;
  minServers: number;
  poolId: string;
  region: string;
  systemReserve: ResourceVector;
};
}

export interface SecretInputBody {
  value: string;
}

export interface SendInputBody {
  context?: {
  environment?: string;
  kind?: string;
  label: string;
  path: string;
  planId?: string;
  projectId?: string;
  service?: string;
};
  message: string;
}

export interface Sent {
  conversation: Conversation;
  task: Task;
}

export interface Server {
  accelerator?: Accelerator;
  accountId: string;
  cloudId: string;
  connectionId: string;
  createdAt: string;
  creationOperationId: string;
  draining: boolean;
  health: RuntimeHealth;
  hourlyPriceKnown?: boolean;
  hourlyUsd?: number;
  id: string;
  name: string;
  orgId: string;
  ownership: string;
  priceCheckedAt: string;
  pricedSize?: string;
  provider: string;
  region: string;
  resourceId: string;
  revision: number;
  runtimeConnectionId: string;
  size: string;
  stateRevision: number;
  status: string;
  updatedAt: string;
}

export interface ServerDefaults {
  baseUrl?: string;
  keySet: boolean;
  model: string;
  provider: string;
}

export interface ServerImage {
  accelerators?: string[];
  architecture?: string;
  bootstrap: boolean;
  id: string;
  minDiskGb?: number;
  name: string;
  regions: string[];
}

export interface ServerInput {
  acknowledgeUnknownCosts?: boolean;
  cloudRevision: number;
  confirmAdoption?: boolean;
  connectionId?: string;
  image?: string;
  kind: string;
  name?: string;
  region?: string;
  serverId?: string;
  serverRevision?: number;
  size?: string;
}

export interface ServerInputBody {
  acknowledgeUnknownCosts?: boolean;
  cloudRevision: number;
  confirmAdoption?: boolean;
  connectionId?: string;
  image?: string;
  kind: string;
  name?: string;
  region?: string;
  serverId?: string;
  serverRevision?: number;
  size?: string;
}

export interface ServerList {
  servers: (Server | null)[];
}

export interface ServerOptions {
  images: ServerImage[];
  regions: ServerRegion[];
  sizes: ServerSize[];
}

export interface ServerRegion {
  id: string;
  name: string;
}

export interface ServerSize {
  accelerator?: Accelerator;
  architectures?: string[];
  cpus: number;
  diskGb: number;
  hourlyPriceKnown?: boolean;
  hourlyUsd: number;
  id: string;
  memoryMib: number;
  monthlyUsd: number;
  regions: string[];
}

export interface ServerUsage {
  draining: boolean;
  reservedCpuMillis: number;
  reservedGpus: number;
  reservedMemoryBytes: number;
  retainedStorage: RetainedPoolStorage[];
  serverId: string;
  workloads: ServerWorkload[];
}

export interface ServerWorkload {
  active: boolean;
  bindingId?: string;
  cpuMillis: number;
  deploymentId?: string;
  environmentId: string;
  executionRevision: number;
  gpus: number;
  memoryBytes: number;
  phase: string;
  planId: string;
  poolId: string;
  projectId: string;
  service: string;
}

export interface Service {
  commands: Commands;
  confidence: string;
  context?: string;
  deployable: boolean;
  ecosystem: string;
  entrypoint: string;
  env?: EnvUse[];
  evidence: DiscoveryEvidence[];
  framework?: string;
  id: string;
  image?: string;
  kind: string;
  name: string;
  path: string;
  ports?: number[];
  requires?: Requirement[];
  runtime?: string;
  source?: string;
  unit?: string;
  unitName?: string;
}

export interface ServiceConfig {
  artifact?: ReusedArtifact;
  build?: string;
  buildTarget?: BuildTarget;
  capacityIntent?: CapacityIntent;
  capacityMode?: string;
  catalog?: Recipe;
  cloudPlacement?: CloudPlacement;
  cloudSqlNetwork?: CloudSQLApplicationNetwork;
  clusterDemand?: ClusterDemand;
  commitSha?: string;
  component?: ComponentConfig;
  context?: string;
  databaseGateway?: DatabaseGatewayConfig;
  ecosystem: string;
  endpoint?: ManifestEndpoint;
  env?: EnvLink[];
  execution: string;
  farp?: FarpConfig;
  framework?: string;
  gateway?: GatewayRoute[];
  gpu?: GPUPlacement;
  healthPath?: string;
  image?: string;
  internal?: boolean;
  port?: number;
  privateEgress?: boolean;
  provider: string;
  public: boolean;
  region?: string;
  repo?: string;
  scale?: Scale;
  service: string;
  size?: Size;
  source: string;
  start?: string;
  target?: string;
  targetAccountIdentity?: string;
  targetSettings?: Record<string, any>;
  worker?: WorkerSettings;
  workload: string;
}

export interface ServiceCoverage {
  name: string;
  provider: string;
  reason?: string;
  watchable: boolean;
}

export interface ServicePort {
  port: number;
  service: string;
}

export interface ServiceTraffic {
  bindingId: string;
  environmentId: string;
  latency: number;
  name: string;
  previousLatency: number;
  previousRequests: number;
  projectId: string;
  provider: string;
  requests: number;
  statistic?: string;
}

export interface SetDefaultPlacementInputBody {
  defaultPlacement: ({
  connectionId: string;
  provider: string;
  region: string;
}) | null;
  revision: number;
}

export interface SetGitLabWebhookInputBody {
  signingToken?: string;
}

export interface SetGrantInputBody {
  projectId?: string;
  role: string;
}

export interface SetTeamInputBody {
  teamId?: string | null;
}

export interface Setting {
  key: string;
  label: string;
  required?: boolean;
}

export interface SettingInfo {
  default?: string;
  name: string;
  pattern: string;
  required: boolean;
  title: string;
}

export interface SettingsInputBody {
  apex: string;
  attach: string;
  defaultLimits: {
  concurrentBuilds: number;
  memoryMib: number;
  vcpus: number;
  vms: number;
};
  dns: {
  provider: string;
  zone: string;
};
  dnsAccountId?: string;
  dnsTeamId?: string;
  dnsToken?: string;
  registry: {
  authEndpoint: string;
  endpoint: string;
  repository: string;
};
  registryPassword?: string;
  registryUsername?: string;
}

export interface SettingsView {
  apex: string;
  attach: string;
  defaultLimits: PlatformLimits;
  dns: PlatformDNSSettings;
  dnsCredentialSaved: boolean;
  registry: RegistrySettings;
  registryCredentialSaved: boolean;
}

export interface SetupRequestBody {
  email: string;
  name: string;
  organizationName: string;
  password: string;
  token?: string;
}

export interface SetupResponse {
  ok: boolean;
  verificationRequired: boolean;
}

export interface SetupStatus {
  pending: boolean;
}

export interface SetupStep {
  body?: string;
  command?: string;
  title: string;
}

export interface SignIn {
  authorizeUrl: string;
  state: string;
}

export interface SignInApp {
  clientId?: string;
  cloud: string;
  connections: number;
  organizations: number;
  problem?: string;
  slug?: string;
  source: string;
  updatedAt?: string;
  updatedBy?: string;
}

export interface SignInAppList {
  apps: SignInApp[];
  callbackUrl: string;
  keyless: KeylessState;
}

export interface SignInInputBody {
  cloud: string;
  connectionId?: string;
  name: string;
  settings?: Record<string, any>;
}

export interface Size {
  cpu: number;
  gpu?: ManifestGPU;
  memory: number;
  shm?: number;
}

export interface SizeEdit {
  cpu: number;
  memory: number;
}

export interface Snapshot {
  cursor: string;
  partial: boolean;
  records: SnapshotRecord[];
}

export interface SnapshotRecord {
  data: any;
  entityId: string;
  event: string;
  id: string;
  orgId: string;
  projectId?: string;
  time: string;
}

export interface Soak {
  duration: number;
  environment: string;
  healthy: boolean;
  reasons: string[];
  running: boolean;
  services: SoakService[];
  since: string;
  unverified: boolean;
}

export interface SoakService {
  name: string;
  provider: string;
  reason: string;
  verdict: string;
}

export interface Source {
  alias: string;
  createdAt: string;
  createdBy: string;
  id: string;
  orgId: string;
  projectId: string;
  push?: PushState;
  ref: string;
  removedAt?: string;
  repository: string;
  root: string;
  updatedAt: string;
}

export interface SourceArtifact {
  alias: string;
  commitSha: string;
  key: string;
  repository: string;
}

export interface SourceCounts {
  all: number;
  farp: number;
  here: number;
}

export interface SourceDiff {
  additions: number;
  aheadBy: number;
  alias: string;
  behindBy: number;
  commits: DiffCommit[];
  deletions: number;
  direction: string;
  error?: string;
  filesChanged: number;
  migrations: MigrationFile[];
  productionCommit: string;
  repository: string;
  sourceId: string;
  stagingCommit: string;
  totalCommits: number;
  truncated: boolean;
}

export interface SourceList {
  sources: DiscoverySource[];
}

export interface SourceRef {
  alias: string;
  analysisId: string;
  commitSha: string;
  repository: string;
  root?: string;
}

export interface SourceState {
  latest?: Analysis;
  shown?: Analysis;
  source: DiscoverySource;
}

export interface SpendReport {
  accounts: AccountSpend[];
  month: string;
  sources: UsageSource[];
  totals: Amount[];
}

export interface StartAnalysisInputBody {
  ref?: string;
  sources?: string[];
}

export interface StartGitLabSignInInputBody {
  hostId: string;
}

export interface StartInputBody {
  context?: {
  environment?: string;
  kind?: string;
  label: string;
  path: string;
  planId?: string;
  projectId?: string;
  service?: string;
};
  message: string;
  projectId?: string;
}

export interface Step {
  actionId: string;
  finishedAt?: string;
  image: string;
  message: string;
  name: string;
  op: string;
  order: number;
  provider: string;
  revision?: string;
  startedAt?: string;
  status: string;
  subject: string;
  url: string;
  warnings?: string[];
}

export interface StorageVolumeList {
  volumes: StoredVolume[];
}

export interface StoredVolume {
  capacityBytes: number;
  connectionId: string;
  environmentId: string;
  id: string;
  instanceId: string;
  name: string;
  projectId: string;
  provider: string;
  role: string;
  status: string;
  storageClass?: string;
}

export interface Sync {
  after?: CloudprofilesTarget;
  afterDefault?: DefaultPlacement;
  attempt: number;
  before?: CloudprofilesTarget;
  beforeDefault?: DefaultPlacement;
  cloudId: string;
  createdAt: string;
  createdBy: string;
  default?: boolean;
  environments: EnvironmentChange[];
  error?: string;
  id: string;
  orgId: string;
  previewId: string;
  projectId: string;
  reverts?: string;
  status: string;
  targetId?: string;
  updatedAt: string;
}

export interface SyncResult {
  generation: number;
  message?: string;
  restarted: boolean;
  restored: number;
  state: string;
}

export interface SyncRoute {
  cutoverId?: string;
  hostname: string;
  message?: string;
  notBefore: string;
  prepareId: string;
  recordType?: string;
  releaseId?: string;
  routeId: string;
  status: string;
  ttl?: number;
  value?: string;
}

export interface TLSRenewalAuthority {
  active: boolean;
  createdAt: string;
  generation: number;
  id: string;
  method: string;
  orgId: string;
  ownerRef: string;
  schemaVersion: number;
  updatedAt: string;
  verificationEvidenceRef: string;
  verifiedAt: string;
  verifiedBy: string;
}

export interface TLSRenewalAuthorityInputBody {
  active: boolean;
  attachmentId: string;
  expectedGeneration: number;
  id?: string;
  method: string;
  ownerRef: string;
  projectId: string;
  verificationEvidenceRef: string;
}

export interface Target {
  cloudId: string;
  connectionId: string;
  createdAt: string;
  displayName: string;
  id: string;
  locationId?: string;
  managedResourceId?: string;
  name: string;
  orgId: string;
  poolId?: string;
  projectId: string;
  provider: string;
  region: string;
  revision: number;
  runtime: string;
  tags: string[];
  updatedAt: string;
}

export interface TargetChoice {
  connectionId: string;
  gpu?: GPURequest;
  provider?: string;
  region?: string;
  sizeId?: string;
  source: string;
}

export interface Task {
  createdAt: string;
  environmentId: string;
  error: string;
  eventSeq: number;
  finishedAt?: string;
  id: string;
  kind: string;
  maxTokens: number;
  metadata: Record<string, any>;
  orgId: string;
  projectId: string;
  releasePlanId?: string;
  releaseRevision?: string;
  requestedBy: string;
  runId: string;
  sessionId: string;
  status: string;
  tokens: number;
  updatedAt: string;
}

export interface TaskEvent {
  createdAt: string;
  environmentId: string;
  error: string;
  eventSeq: number;
  finishedAt?: string;
  id: string;
  kind: string;
  maxTokens: number;
  orgId: string;
  projectId: string;
  requestedBy: string;
  runId: string;
  status: string;
  tokens: number;
  updatedAt: string;
}

export interface Team {
  createdAt: string;
  description: string;
  grants: TeamGrant[];
  id: string;
  members: TeamMember[];
  name: string;
  orgId: string;
  slug: string;
  updatedAt: string;
}

export interface TeamGrant {
  id: string;
  projectId: string;
  role: string;
}

export interface TeamList {
  teams: Team[];
}

export interface TeamMember {
  addedAt: string;
  email: string;
  name: string;
  userId: string;
}

export interface Template {
  compatibility: Compatibility;
  config?: Record<string, any>;
  jobs?: Record<string, any>;
  manifest: Manifest;
  parameters?: Record<string, any>;
  profiles: Record<string, any>;
  requiredCapabilities?: string[];
  resources?: Record<string, any>;
  schemaVersion: number;
  secrets?: Record<string, any>;
}

export interface TemplateRelease {
  createdAt: string;
  digest: string;
  fleetId: string;
  id: string;
  template: Template;
}

export interface TenantRow {
  cleanupBlocked?: CleanupBlock;
  limits: PlatformLimits;
  orgId: string;
  orgName: string;
  tenant?: PlatformcloudTenant;
  use: PlatformcloudUse;
}

export interface TestPushResult {
  status: string;
}

export interface Thresholds {
  errorRate: number;
  errorRateDelta: number;
  minRequests: number;
  p95LatencyRatio: number;
}

export interface TimeoutVerificationInputBody {
  attachmentId: string;
  configRevision: string;
  drainPolicy: {
  maxConnectionLifetimeSeconds?: number;
  mode: string;
  terminationGraceSeconds: number;
};
  evidenceRef: string;
  expectedAttachmentRevision: number;
}

export interface TrafficReport {
  end: string;
  previousTotal: number;
  range: string;
  requests: Point[];
  services: ServiceTraffic[];
  sources: UsageSource[];
  start: string;
  step: number;
  total: number;
}

export interface Turn {
  context?: PageContext;
  createdAt: string;
  error: string;
  eventSeq: number;
  events: Event[];
  finishedAt?: string;
  message: string;
  status: string;
  taskId: string;
}

export interface TwoStep {
  enabled: boolean;
}

export interface UnreachableAccount {
  connectionId: string;
  message: string;
  name: string;
}

export interface UnroutableService {
  reason: string;
  service: string;
}

export interface UnwatchedService {
  name: string;
  provider: string;
  reason: string;
}

export interface UpdateDeviceInputBody {
  quietHours: {
  set: boolean;
  value?: QuietHours;
};
  settings?: Record<string, any>;
  timeZone?: string;
}

export interface UpdateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name?: string;
}

export interface UpdateSourceInputBody {
  ref?: string;
  root?: string;
}

export interface UpdateTargetInputBody {
  displayName: string;
  revision: number;
  tags?: string[];
}

export interface UpdateTeamInputBody {
  description?: string;
  name?: string;
}

export interface UpdateVariableInputBody {
  secret?: boolean;
  value: string;
}

export interface Usage {
  cloudId: string;
  environments: EnvironmentUsage[];
}

export interface UsageSource {
  cloud: string;
  connectionId: string;
  name: string;
  reason?: string;
  services?: string[];
  status: string;
}

export interface UseOrganizationInputBody {
  orgId: string;
}

export interface Variable {
  createdAt: string;
  createdBy: string;
  environment: string;
  id: string;
  key: string;
  orgId: string;
  projectId?: string;
  scope: string;
  secret: boolean;
  service?: string;
  updatedAt: string;
  updatedBy: string;
  value: string;
}

export interface VariableList {
  variables: Variable[];
}

export interface View {
  allocatedUsd: string;
  allocations: (Allocation | null)[];
  canManage: boolean;
  embeddings: Embeddings;
  huggingface?: HuggingFaceToken;
  monthlyBudgetUsd: string;
  orgId: string;
  providerKeys: (ProviderKey | null)[];
  revision: number;
  unallocatedUsd: string;
  updatedAt: string;
  updatedBy?: string;
}

export interface Volume {
  fsGroup?: number;
  kubernetesSizeMb?: number;
  mountPath: string;
}

export interface Watch {
  boundAt: string;
  checkedAt?: string;
  createdAt: string;
  deploymentId: string;
  endsAt: string;
  environment: string;
  environmentId: string;
  held: HeldRoute[];
  heldReleased: boolean;
  id: string;
  orgId: string;
  planId: string;
  projectId: string;
  reason: string;
  rollbackApprovedAt?: string;
  rollbackPlanId: string;
  services: WatchedService[];
  settledAt?: string;
  startsAt: string;
  status: string;
  thresholds: Thresholds;
  unverified: boolean;
  unwatched: UnwatchedService[];
  updatedAt: string;
  verdict: string;
}

export interface WatchedService {
  bindingId: string;
  checkedAt?: string;
  name: string;
  op: string;
  provider: string;
  reason: string;
  revision: string;
  verdict: string;
}

export interface WebhookEndpoint {
  createdAt: string;
  description: string;
  enabled: boolean;
  id: string;
  url: string;
}

export interface WebhookEndpointInputBody {
  description: string;
  url: string;
}

export interface WebhookEndpointList {
  items: WebhookEndpoint[];
}

export interface WebhookEndpointSecret {
  endpoint: WebhookEndpoint;
  secret: string;
}

export interface WeightCache {
  kubernetesSizeMb: number;
  mountPath: string;
  sizeSetting?: string;
}

export interface WorkerSettings {
  compatibilityDate: string;
  compatibilityFlags?: string[];
  durableObjects?: DurableObjectClass[];
  main: string;
}

export interface WorkloadCapabilities {
  lifecycleJobs: boolean;
  podGroups: boolean;
  revision: string;
  scalingSignals?: string[];
  scratchVolumes: boolean;
}

export interface Workspace {
  ecosystem: string;
  evidence: DiscoveryEvidence[];
  members: string[];
  path: string;
  source?: string;
  tool: string;
}

export interface WorkspaceBudgetInputBody {
  monthlyBudgetUsd: string;
  revision: number;
}

export interface WorkspacePlacement {
  environment: string;
  project: string;
  region: string;
  service: string;
  state: string;
  waitingCapacity: boolean;
}

export interface WorkspaceRegion {
  displayName: string;
  name: string;
  status: string;
}

export interface WorkspaceView {
  enabled: boolean;
  limits: PlatformLimits;
  placements: WorkspacePlacement[];
  regions: WorkspaceRegion[];
  suspendReasons: string[];
  use: PlatformcloudUse;
}

export interface Zone {
  accountId?: string;
  challengeConnectionId?: string;
  connectionGeneration?: number;
  connectionId?: string;
  id: string;
  mode: string;
  name: string;
  orgId: string;
  projectIds: string[];
  proofCheckedAt: string;
  proofExpiresAt: string;
  proofMissingAt: string;
  proofName?: string;
  proofPublishAttempted?: boolean;
  proofPublishAttemptedAt: string;
  proofPublishError?: string;
  proofRecord?: DNSRecord;
  proofValue?: string;
  provider?: string;
  providerZone: DNSZone;
  recordOwnership: boolean;
  registrationId?: string;
  revision: number;
  verifiedAt: string;
}

export interface ZoneInputBody {
  connectionId?: string;
  mode: string;
  name: string;
  projectIds: string[];
}

export interface ZoneList {
  zones: (Zone | null)[];
}

export interface ZoneOverview {
  hostnames: HostnameUse[];
  parentZoneId?: string;
  proofZoneId?: string;
  registrationId?: string;
  state: string;
  subdomains: string[];
  wildcards: boolean;
  zone?: Zone;
}

export interface ZoneUpdateBody {
  projectIds: string[];
  renewProof: boolean;
  revision: number;
}

export interface fleetRequestError {
  code: number;
  error: string;
}

export interface AuthConfig {
  bearerToken?: string;
  apiKey?: string;
  customHeaders?: Record<string, string>;
}

export interface ClientConfig {
  baseURL: string;
  auth?: AuthConfig;
  timeout?: number;
}


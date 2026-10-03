export const statuses = ['operational', 'degraded', 'outage', 'planned', 'retired'] as const;
export type ServiceStatus = (typeof statuses)[number];
export const criticalities = ['low', 'medium', 'high', 'critical'] as const;
export type Criticality = (typeof criticalities)[number];
export const targetTypes = ['service', 'vm', 'asset', 'location'] as const;
export type TargetType = (typeof targetTypes)[number];
export const statusReasons = [
  'incident',
  'maintenance',
  'recovered',
  'rollout',
  'correction',
  'other',
] as const;
export const retireReasons = [
  'replaced',
  'decommissioned',
  'merged',
  'error_correction',
  'other',
] as const;
export const removalReasons = [
  'no_longer_needed',
  'replaced',
  'error_correction',
  'other',
] as const;
export type Service = {
  id: string;
  reference: string;
  name: string;
  description: string | null;
  ownerUserId: string | null;
  ownerTeamId: string | null;
  supportTeamId: string | null;
  criticality: Criticality;
  status: ServiceStatus;
  statusReason: string | null;
  retiredAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};
export type ServiceNode = {
  type: TargetType;
  id: string;
  name?: string;
  reference?: string;
  status?: string;
  criticality?: Criticality;
  missing?: boolean;
  hidden?: boolean;
};
export type ServiceLink = {
  relationshipId: string;
  type: string;
  confidence: string;
  since: string;
  node: ServiceNode;
};
export type ServiceDetail = Service & {
  dependencies: ServiceLink[];
  dependents: ServiceLink[];
  dependenciesTruncated: boolean;
  dependentsTruncated: boolean;
  dependenciesNextCursor?: string;
  dependentsNextCursor?: string;
};
export type ServiceLinkList = { items: ServiceLink[]; nextCursor?: string };
export type ServiceList = { items: Service[]; nextCursor?: string };
export type ImpactNode = ServiceNode & {
  depth: number;
  confidence: string;
  path: {
    relationshipId: string;
    fromType: string;
    fromId: string;
    toType: string;
    toId: string;
    type: string;
    confidence: string;
  }[];
};
export type Impact = {
  start: ServiceNode;
  direction: 'downstream' | 'upstream';
  maxDepth: number;
  items: ImpactNode[];
  truncated: boolean;
  depthLimited: boolean;
  nodeLimited: boolean;
};

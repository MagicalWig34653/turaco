// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type Approval = {
  id: string;
  subjectType: string;
  subjectId: string;
  subjectLabel: string;
  stepIndex: number;
  status: 'pending' | 'approved' | 'rejected' | 'cancelled';
  approverUserId: string | null;
  approverTeamId: string | null;
  decidedByUserId: string | null;
  decidedAt: string | null;
  decisionComment: string | null;
  version: number;
  createdAt: string;
};

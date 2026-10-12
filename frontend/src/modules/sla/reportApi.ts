import { api } from '../../platform/api/client';
import type { ReportGroupBy, SlaReport } from './slaModel';

export const slaReportApi = {
  /** GET /sla/report: finished clocks of the Queues the caller may view; UTC days, at most 366. */
  get: (
    params: { from: string; to: string; groupBy: ReportGroupBy; queueId?: string },
    signal?: AbortSignal,
  ) =>
    api.get<SlaReport>('/sla/report', {
      signal,
      query: { from: params.from, to: params.to, groupBy: params.groupBy, queueId: params.queueId },
    }),
};

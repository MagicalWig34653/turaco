import type { RouteId } from '../../app/routes';

// Each destination has a recognizable outline. The rail never relies on color alone.
const paths: Partial<Record<RouteId, string>> = {
  home: 'M3 11 12 3l9 8M5 10v11h14V10M10 21v-6h4v6',
  me: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM4 21v-2a8 8 0 0 1 16 0v2',
  myWork: 'M4 7h16v14H4zM9 7V4h6v3M8 14l2 2 5-5',
  notifications: 'M6 9a6 6 0 0 1 12 0c0 6 3 8 3 9H3c0-1 3-3 3-9ZM10 21h4',
  catalog: 'M4 5h16v16H4zM4 10h16M9 5v16',
  requests: 'M6 3h9l4 4v14H6zM14 3v5h5M9 13h7m-7 4h5',
  approvals: 'M5 4h14v16H5zM8 12l3 3 5-6',
  briefing: 'M4 4h16v16H4zM8 8h8M8 12h8M8 16h5',
  myChanges: 'M4 6h16M4 12h10M4 18h16M17 9l3 3-3 3',
  myInitiatives: 'M4 20V5h16v15M8 16l4-6 4 3 4-5',
  myAssets: 'M4 7h16v13H4zM4 7l4-4h8l4 4M9 13h6',
  myTickets: 'M4 5h16v11H9l-5 4zM8 9h8m-8 3h5',
  knowledge: 'M3 5h8a3 3 0 0 1 2 1 3 3 0 0 1 2-1h6v15h-6a3 3 0 0 0-2 1 3 3 0 0 0-2-1H3zM12 6v15',
  incidents: 'M12 3 2 21h20zM12 9v5m0 3v1',
  tasks: 'M5 4h14v16H5zM8 9l1 1 2-2m2 1h3M8 15l1 1 2-2m2 1h3',
  assets: 'M4 7h16v13H4zM7 7V4h10v3M9 12h6m-6 4h4',
  stock: 'M3 8l9-5 9 5v12H3zM3 8l9 5 9-5M12 13v7',
  warehouses: 'M2 10 12 3l10 7v11H2zM7 21v-8h10v8',
  reservations: 'M5 4h14v17H5zM8 8h8m-8 4h8m-8 4h4M15 15l2 2 3-4',
  ledger: 'M4 3h16v18H4zM8 8h8m-8 4h8m-8 4h5',
  receipts: 'M4 4h16v16H4zM12 6v9m-4-4 4 4 4-4M8 18h8',
  orders: 'M5 4h14l2 17H3zM9 9V6a3 3 0 0 1 6 0v3',
  procurementRequests: 'M4 5h16v14H4zM8 9h8m-8 4h8m-8 3h4',
  suppliers: 'M3 21V8l6-4v17m0-12 6-4v16m0-12 6-3v15M5 12h2m4 0h2m4 0h2',
  ticketQueue: 'M3 5h18v5a2 2 0 0 0 0 4v5H3v-5a2 2 0 0 0 0-4zM12 5v14',
  problems: 'M12 3a8 8 0 0 0-5 14v4h10v-4a8 8 0 0 0-5-14ZM9 21h6M12 7v5m0 3v1',
  runbooks: 'M4 4h16v16H4zM8 9h8M8 13h8M8 17h5',
  devices: 'M3 5h18v12H3zM9 21h6m-3-4v4',
  endpointFindings: 'M4 5h16v12H4zM9 21h6m-3-4v4M12 8v4m0 2v1',
  managementArtifacts: 'M4 4h16v16H4zM8 8h8v8H8zM12 8v8',
  managementFilters: 'M3 5h18M6 12h12M9 19h6M8 3v4m8 3v4m-4 3v4',
  infrastructureTree: 'M12 3v6M5 9h14M5 9v5m14-5v5M2 14h6v6H2zm8 0h6v6h-6zm8 0h4v6h-4z',
  infrastructureVMs: 'M3 4h18v14H3zM7 21h10M8 9l3 3-3 3m5 0h4',
  services:
    'M12 3v3m0 12v3M3 12h3m12 0h3M5.6 5.6l2.1 2.1m8.6 8.6 2.1 2.1M18.4 5.6l-2.1 2.1m-8.6 8.6-2.1 2.1M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z',
  changes: 'M4 7h16M4 12h16M4 17h16M8 4v6m8 4v6',
  initiatives: 'M4 20V4h16v16M8 15l4-6 3 3 4-5',
  maintenanceCalendar: 'M4 6h16v15H4zM4 10h16M8 3v5m8-5v5M8 14h3m3 0h3',
  securityOverview: 'M12 3 4 6v5c0 5 3 8 8 10 5-2 8-5 8-10V6zM9 12l2 2 4-4',
  securityAdvisories: 'M12 3 4 6v6c0 4 3 7 8 9 5-2 8-5 8-9V6zM12 8v5m0 3v1',
  securityFindings: 'M4 4h16v16H4zM8 9h8m-8 4h8m-8 4h4M17 16l2 2 2-3',
  products: 'M3 7l9-4 9 4v12l-9 3-9-3zM3 7l9 4 9-4m-9 4v11',
  catalogAdmin: 'M4 4h16v16H4zM8 8h8M8 12h8m-8 4h5M17 15v4m-2-2h4',
  allRequests: 'M5 3h12l3 3v15H5zM16 3v4h4M9 11h7m-7 4h7',
  roles: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM4 21a8 8 0 0 1 16 0M16 6h5m-2-2v4',
  roleAssignments: 'M4 4h16v16H4zM8 9h8m-8 4h5m-5 4h4M15 15l2 2 3-4',
  directorySync: 'M4 10a8 8 0 0 1 14-4l2 2M20 5v4h-4M20 14a8 8 0 0 1-14 4l-2-2M4 19v-4h4',
  audit: 'M5 3h14v18H5zM8 8h8m-8 4h8m-8 4h5M16 15l2 2 3-4',
  recurrence: 'M4 12a8 8 0 1 1 3 6M4 17v-5h5M12 8v5l3 2',
};

export function NavIcon({ id }: { id: RouteId }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="18"
      height="18"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[id] ?? 'M4 4h16v16H4zM8 12h8'} />
    </svg>
  );
}

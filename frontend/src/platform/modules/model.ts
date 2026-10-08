export const categories = [
  'core',
  'service_management',
  'assets_inventory',
  'endpoints_security',
  'infrastructure_operations',
  'workforce',
  'insight',
] as const;
export const reasonCodes = [
  'initial_setup',
  'business_need',
  'not_needed',
  'maintenance',
  'compliance_review',
  'evaluation',
] as const;
export type ReasonCode = (typeof reasonCodes)[number];
export type Module = {
  key: string;
  nameKey: string;
  descriptionKey: string;
  category: (typeof categories)[number];
  core: boolean;
  enabled: boolean;
  switchOn: boolean;
  state: 'enabled' | 'disabled' | 'blocked';
  blockedReason:
    | 'startup_gate_off'
    | 'dpia_not_recorded'
    | 'no_enabled_provider'
    | 'no_providers_configured'
    | 'dependency_disabled'
    | 'runtime_setting_off'
    | null;
  requires: string[];
  requiredBy: string[];
  startupGates: string[];
  version: number;
};
export type ModuleEnabled = (key: string) => boolean;
/** Frontend route ownership. Settings remain reachable to resolve runtime preconditions. */
const paths: Record<string, readonly string[]> = {
  servicedesk: ['/support', '/service-desk', '/incidents', '/problems'],
  knowledge: ['/knowledge', '/runbooks'],
  catalog: ['/catalog', '/admin/catalog'],
  requests: ['/requests', '/admin/requests'],
  products: ['/admin/products', '/products'],
  assets: ['/assets', '/my-assets'],
  procurement: ['/procurement'],
  inventory: ['/inventory'],
  endpoints: [
    '/devices',
    '/endpoints',
    '/endpoint-findings',
    '/management-artifacts',
    '/management-filters',
    '/software',
    '/deployments',
    '/target-sets',
  ],
  security: ['/security'],
  remoteaccess: ['/remote-access'],
  infrastructure: ['/infrastructure'],
  services: ['/services', '/impact'],
  changes: ['/changes'],
  planning: ['/initiatives', '/maintenance-calendar'],
  presence: ['/presence'],
  briefing: ['/briefing'],
};
export function moduleForPath(path: string): string | undefined {
  const pathname = path.split(/[?#]/)[0]!;
  return Object.entries(paths).find(([, prefixes]) =>
    prefixes.some((prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`)),
  )?.[0];
}
export function pathEnabled(path: string, enabled: ModuleEnabled): boolean {
  const key = moduleForPath(path);
  return !key || enabled(key);
}
export function statusEnabled(
  items: readonly { key: string; enabled: boolean }[] | undefined,
): ModuleEnabled {
  const enabled = new Set(items?.filter((item) => item.enabled).map((item) => item.key));
  return (key) => enabled.has(key);
}
export function switchRequest(module: Module, reasonCode: ReasonCode) {
  return { expectedVersion: module.version, reasonCode };
}
export function switchAction(module: Module) {
  return module.switchOn ? 'disable' : 'enable';
}
export function filterModules(
  items: readonly Module[],
  query: string,
  category: string,
  state: string,
  text: (module: Module) => string,
) {
  const needle = query.trim().toLocaleLowerCase();
  return items.filter(
    (item) =>
      (!category || item.category === category) &&
      (!state || item.state === state) &&
      (!needle || `${item.key} ${text(item)}`.toLocaleLowerCase().includes(needle)),
  );
}
export function settingsPath(key: string): string | undefined {
  return key === 'presence' || key === 'ai' ? `/admin/${key}` : undefined;
}

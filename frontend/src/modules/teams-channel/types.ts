// Types mirror api/openapi/openapi.yaml (TeamsChannelRoutes).

export type AdapterMode = 'real' | 'fake' | 'not_configured';

export type ChannelRoute = {
  id: string;
  category: string;
  destinationKey: string;
  createdBy: string;
  createdAt: string;
};

export type ChannelRoutes = {
  items: ChannelRoute[];
  mode: AdapterMode;
  /** Configured destination keys; webhook URLs are never returned. */
  destinations: string[];
  /** Broadcastable notification categories. */
  categories: string[];
};

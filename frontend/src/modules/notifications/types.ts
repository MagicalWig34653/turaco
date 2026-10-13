// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type AppNotification = {
  id: string;
  category: string;
  params: Record<string, unknown>;
  linkType: string | null;
  linkId: string | null;
  createdAt: string;
  readAt: string | null;
};

export type UnreadCount = { count: number; max: number };

export type EmailPreference = { category: string; channel: 'email'; enabled: boolean };
/** The personal Microsoft Teams card of a category (only categories that carry one are listed). */
export type TeamsPreference = { category: string; channel: 'teams'; enabled: boolean };
export type NotificationPreference = EmailPreference | TeamsPreference;

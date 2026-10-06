import {
  registerNotificationCategories,
  registerNotificationLink,
} from '../modules/notifications/text';

/**
 * Notification categories and link targets of the modules, registered once at start-up. The
 * notifications module itself knows none of its producers.
 */
export function registerModuleNotifications(): void {
  registerNotificationCategories({
    'task.assigned': {
      textKey: 'notifications.task.assigned',
      labelKey: 'notifications.category.task.assigned',
    },
    'task.completed': {
      textKey: 'notifications.task.completed',
      labelKey: 'notifications.category.task.completed',
    },
    'ticket.assigned': {
      textKey: 'notifications.ticket.assigned',
      labelKey: 'notifications.category.ticket.assigned',
    },
    'ticket.comment': {
      textKey: 'notifications.ticket.comment',
      labelKey: 'notifications.category.ticket.comment',
    },
    'ticket.resolved': {
      textKey: 'notifications.ticket.resolved',
      labelKey: 'notifications.category.ticket.resolved',
    },
    'majorincident.update': {
      textKey: 'notifications.majorincident.update',
      labelKey: 'notifications.category.majorincident.update',
    },
    'change.scheduled': {
      textKey: 'notifications.change.scheduled',
      labelKey: 'notifications.category.change.scheduled',
    },
    'change.reminder': {
      textKey: 'notifications.change.reminder',
      labelKey: 'notifications.category.change.reminder',
    },
    'change.state': {
      textKey: 'notifications.change.state',
      labelKey: 'notifications.category.change.state',
    },
    'initiative.state': {
      textKey: 'notifications.initiative.state',
      labelKey: 'notifications.category.initiative.state',
    },
    'security.advisory': {
      textKey: 'notifications.security.advisory',
      labelKey: 'notifications.category.security.advisory',
    },
    'security.risk_review_due': {
      textKey: 'notifications.security.risk_review_due',
      labelKey: 'notifications.category.security.risk_review_due',
    },
    'software.approval_requested': {
      textKey: 'notifications.software.approval_requested',
      labelKey: 'notifications.category.software.approval_requested',
    },
    'asset.assigned': {
      textKey: 'notifications.asset.assigned',
      labelKey: 'notifications.category.asset.assigned',
    },
    'approval.requested': {
      textKey: 'notifications.approval.requested',
      labelKey: 'notifications.category.approval.requested',
    },
    'request.approved': {
      textKey: 'notifications.request.approved',
      labelKey: 'notifications.category.request.approved',
    },
    'request.rejected': {
      textKey: 'notifications.request.rejected',
      labelKey: 'notifications.category.request.rejected',
    },
    'request.completed': {
      textKey: 'notifications.request.completed',
      labelKey: 'notifications.category.request.completed',
    },
  });
  registerNotificationLink('initiative', (id) => `/initiatives/${encodeURIComponent(id)}`);
  registerNotificationLink(
    'security_advisory',
    (id) => `/security/advisories/${encodeURIComponent(id)}`,
  );
  registerNotificationLink(
    'security_finding',
    (id) => `/security/findings/${encodeURIComponent(id)}`,
  );
  registerNotificationLink(
    'software_version',
    (id) => `/software/versions/${encodeURIComponent(id)}`,
  );
  registerNotificationLink('change', (id) => `/changes/${encodeURIComponent(id)}`);
  registerNotificationLink('major_incident', (id) => `/incidents/${encodeURIComponent(id)}`);
  registerNotificationLink('ticket', (id) => `/support/${encodeURIComponent(id)}`);
  registerNotificationLink('asset', (id) => `/assets/${encodeURIComponent(id)}`);
  registerNotificationLink('approval', (id) => `/approvals/${encodeURIComponent(id)}`);
  registerNotificationLink('service_request', (id) => `/requests/${encodeURIComponent(id)}`);
  registerNotificationLink('task', (id) => `/tasks/${encodeURIComponent(id)}`);
}

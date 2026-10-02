import { describe, expect, it } from 'vitest';
import { translate } from '../../platform/i18n/i18n';
import { notificationLink, notificationText, unreadLabel } from './text';
import type { AppNotification } from './types';

const base: AppNotification = {
  id: '1',
  category: 'task.assigned',
  params: { title: 'Replace toner' },
  linkType: 'task',
  linkId: 'abc',
  createdAt: '2026-10-02T10:00:00Z',
  readAt: null,
};

const t = (key: Parameters<typeof translate>[1], params?: Record<string, string | number>) =>
  translate('en', key, params);

describe('notificationText', () => {
  it('localizes known categories with the task title', () => {
    expect(notificationText(t, base)).toContain('Replace toner');
    expect(notificationText(t, { ...base, category: 'task.completed' })).toContain('Replace toner');
  });

  it('falls back to a generic text for unknown categories and missing params', () => {
    expect(notificationText(t, { ...base, category: 'future.thing' })).toBe(
      translate('en', 'notifications.generic'),
    );
    expect(notificationText(t, { ...base, params: {} })).not.toContain('undefined');
  });

  it('is available in German', () => {
    expect(translate('de', 'notifications.task.assigned', { title: 'X' })).toContain('X');
  });
});

describe('notificationLink', () => {
  it('links tasks and encodes the id', () => {
    expect(notificationLink(base)).toBe('/tasks/abc');
    expect(notificationLink({ ...base, linkId: 'a/b' })).toBe('/tasks/a%2Fb');
  });
  it('has no link for unknown targets', () => {
    expect(notificationLink({ ...base, linkType: 'asset' })).toBeUndefined();
    expect(notificationLink({ ...base, linkType: null, linkId: null })).toBeUndefined();
  });
});

describe('unreadLabel', () => {
  it('shows an overflow marker at the server cap', () => {
    expect(unreadLabel(3, 100)).toBe('3');
    expect(unreadLabel(99, 100)).toBe('99');
    expect(unreadLabel(100, 100)).toBe('99+');
  });
});

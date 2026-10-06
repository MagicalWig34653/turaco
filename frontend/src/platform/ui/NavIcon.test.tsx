import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { appRoutes } from '../../app/routes';
import { NavIcon } from './NavIcon';

describe('navigation icons', () => {
  it('gives every navigation destination its own outline', () => {
    const icons = appRoutes
      .filter((route) => route.nav)
      .map((route) => renderToStaticMarkup(<NavIcon id={route.id} />));
    expect(new Set(icons).size).toBe(icons.length);
  });
});

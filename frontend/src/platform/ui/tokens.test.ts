import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8');

function tokensFor(theme: 'turaco' | 'dark' | 'cyberpunk'): Record<string, string> {
  const base = css.match(/:root,\s*html\[data-theme='turaco'\] \{([^}]+)\}/)?.[1] ?? '';
  const override =
    theme === 'turaco'
      ? ''
      : (css.match(new RegExp(`html\\[data-theme='${theme}'\\] \\{([^}]+)\\}`))?.[1] ?? '');
  const values: Record<string, string> = {};
  for (const match of (base + override).matchAll(/(--[\w-]+):\s*(#[0-9a-f]{6})\s*;/g)) {
    values[match[1] as string] = match[2] as string;
  }
  return values;
}

function luminance(hex: string): number {
  const channels = hex
    .slice(1)
    .match(/../g)!
    .map((value) => {
      const channel = parseInt(value, 16) / 255;
      return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
    });
  return channels[0]! * 0.2126 + channels[1]! * 0.7152 + channels[2]! * 0.0722;
}

function contrast(a: string, b: string): number {
  const values = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (values[0]! + 0.05) / (values[1]! + 0.05);
}

describe('semantic token contrast', () => {
  it.each(['turaco', 'dark', 'cyberpunk'] as const)('%s has AA body and state pairs', (theme) => {
    const tokens = tokensFor(theme);
    const pairs = [
      ...['canvas', 'base', 'raised', 'muted-token', 'hover', 'selected'].flatMap((surface) =>
        ['primary', 'secondary'].map((text) => [`--text-${text}`, `--surface-${surface}`]),
      ),
      ['--rail-text', '--rail-bg'],
      ['--rail-muted', '--rail-bg'],
      ['--rail-text', '--rail-active'],
      ['--rail-muted', '--rail-active'],
      ['--on-accent', '--accent-hover'],
      ['--accent', '--surface-base'],
      ['--on-accent', '--accent'],
      ...(theme === 'cyberpunk'
        ? ['cyan', 'pink', 'yellow', 'green', 'orange'].flatMap((color) =>
            ['base', 'raised', 'hover'].map((surface) => [
              `--cyber-${color}`,
              `--surface-${surface}`,
            ]),
          )
        : []),
      ...(['critical', 'warning', 'info', 'success', 'unknown'] as const).map((state) => [
        `--state-${state}`,
        `--state-${state}-bg`,
      ]),
    ];
    for (const [foreground, background] of pairs) {
      if (!foreground || !background) throw new Error('Invalid token pair');
      expect(
        contrast(tokens[foreground]!, tokens[background]!),
        `${theme}: ${foreground} on ${background}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });
});

// Visible focus and control edges must remain distinguishable without decorative glow.
describe('non-text contrast', () => {
  it.each(['turaco', 'dark', 'cyberpunk'] as const)(
    '%s exposes focus and control boundaries',
    (theme) => {
      const tokens = tokensFor(theme);
      for (const surface of ['canvas', 'base', 'raised', 'muted-token', 'hover', 'selected']) {
        for (const foreground of ['--focus-ring', '--border-strong-token']) {
          expect(
            contrast(tokens[foreground]!, tokens[`--surface-${surface}`]!),
            `${theme}: ${foreground} on ${surface}`,
          ).toBeGreaterThanOrEqual(3);
        }
      }
    },
  );
});

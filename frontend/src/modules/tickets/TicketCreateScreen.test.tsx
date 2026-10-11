import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { DeviceChoice } from './TicketCreateScreen';

describe('DeviceChoice', () => {
  it('distinguishes devices with the same product name by serial and location', () => {
    const html = renderToStaticMarkup(
      <DeviceChoice
        asset={{ id: 'a', reference: 'AST-000001', serialNumber: 'DM-21-01' }}
        name="Befundmonitor DM-21"
        locationName="Station 3"
        selected={false}
        onSelect={() => {}}
      />,
    );
    expect(html).toContain('DM-21-01');
    expect(html).toContain('Station 3');
  });
});

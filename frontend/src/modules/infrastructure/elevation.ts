import type { Placement } from './types';
export type ElevationRow = { unit: number; front?: Placement; rear?: Placement };
export function buildElevation(heightU: number, placements: Placement[]): ElevationRow[] {
  const rows = Array.from(
    { length: Math.max(0, heightU) },
    (_, index) => ({ unit: heightU - index }) as ElevationRow,
  );
  for (const placement of placements) {
    if (placement.removedAt) continue;
    for (const row of rows) {
      if (row.unit >= placement.uPosition && row.unit < placement.uPosition + placement.heightU)
        row[placement.face] = placement;
    }
  }
  return rows;
}

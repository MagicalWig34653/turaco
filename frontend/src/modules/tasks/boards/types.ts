// Types mirror the TaskBoard* schemas of api/openapi/openapi.yaml. Keep them in sync with the contract.
import type { Node } from '../../../platform/ui/query/filterModel';
import type { Task, TaskStatus } from '../types';

export type BoardSwimlane = 'none' | 'assignee';
export type BoardAccess = 'owner' | 'edit' | 'use';

export type BoardColumn = {
  id: string;
  position: number;
  title: string;
  mapsTo: TaskStatus;
  collapsed: boolean;
  wipLimit: number | null;
};

export type BoardFilter = { v: 1; root?: Node; search?: string };

export type TaskBoard = {
  id: string;
  viewId: string;
  viewVersion: number;
  name: string;
  description: string;
  ownerId: string;
  ownerName?: string;
  ownerTeamId: string | null;
  ownerTeamName: string | null;
  swimlane: BoardSwimlane;
  version: number;
  access: BoardAccess;
  canEdit: boolean;
  archived: boolean;
  filter: BoardFilter | null;
  columns: BoardColumn[];
  createdAt: string;
  updatedAt: string;
};

export type BoardCard = Task & { rank: string | null };

export type ColumnCards = {
  columnId: string;
  items: BoardCard[];
  nextCursor?: string;
  count: number;
  countCapped: boolean;
  wipLimit: number | null;
  overWipLimit: boolean;
  warnings?: { code: string; path: string }[];
};

export type BoardColumnDraft = {
  title: string;
  mapsTo: TaskStatus;
  collapsed?: boolean;
  wipLimit?: number | null;
};

export type BoardCreateBody = {
  name: string;
  description?: string;
  filter?: BoardFilter;
  swimlane?: BoardSwimlane;
  columns?: BoardColumnDraft[];
};

export type BoardUpdateBody = {
  expectedVersion: number;
  name?: string;
  description?: string;
  swimlane?: BoardSwimlane;
  filter?: BoardFilter | null;
};

export type ColumnOperation =
  | { operation: 'add'; title: string; mapsTo: TaskStatus; position?: number }
  | { operation: 'rename'; columnId: string; title: string }
  | { operation: 'remap'; columnId: string; mapsTo: TaskStatus }
  | { operation: 'reorder'; columnId: string; position: number }
  | { operation: 'remove'; columnId: string }
  | { operation: 'set_wip'; columnId: string; wipLimit: number | null }
  | { operation: 'collapse'; columnId: string; collapsed: boolean };

/** Where a moved card lands in the target column: below another card, or first. */
export type Anchor = { afterTaskId: string } | { top: true };

export type MoveBody = {
  taskId: string;
  columnId: string;
  expectedVersion: number;
  reason?: string;
} & Partial<{ afterTaskId: string; top: boolean }>;

export type MoveResult = {
  task: Task;
  operation?: 'start' | 'block' | 'unblock' | 'complete' | 'cancel' | 'reopen';
  placed: boolean;
  rank: string | null;
};

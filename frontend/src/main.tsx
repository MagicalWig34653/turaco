import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './app/App';
import { registerModuleNotifications } from './app/notificationCategories';
import './platform/ui/tokens.css';
import './app/app.css';
import './platform/ui/workspace.css';
import './platform/ui/collections.css';
import './modules/changes/changes.css';
import './modules/tickets/report-problem.css';
// Screen refinements follow the shared foundation so equal-specificity rules are predictable.
import './platform/ui/shell/shell.css';
import './modules/my-work/work-dashboard.css';
import './modules/tickets/ticket-workspace.css';

registerModuleNotifications();

const root = document.getElementById('root');
if (!root) {
  throw new Error('Root element not found');
}

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

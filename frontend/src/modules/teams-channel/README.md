# Teams channel routes (F15 slice T-A)

Screen at `/admin/teams-channels` in the Administration group (`integrations.teams.manage`; the backend remains
authoritative). It lists, per broadcastable notification category, the Teams channel destinations that receive a
reference-only post, and adds or removes routes (`GET|POST /integrations/teams/channel-routes`,
`DELETE .../{id}`).

- Only destination keys are shown. Webhook URLs live in the deployment secret file
  `TEAMS_CHANNEL_DESTINATIONS_FILE` and never reach the API or the browser.
- The screen is reachable while the optional module `teams` is off so routes can be prepared; posts are created
  only while the module is on (module switch in `/admin/modules`).
- "Test a destination" sends one clearly marked test card per click (`POST /integrations/teams/destinations/{key}/test-send`);
  the result is shown as delivered or as a machine code. The server allows five per destination in ten minutes
  (429 `notifications.test_send_rate_limited`).
- Adapter mode (`real`, `fake`, `not_configured`) comes from the API and is repeated on the Integrations page.

# Teams channel routes (F15 slice T-A)

Screen at `/admin/teams-channels` in the Administration group (`integrations.teams.manage`; the backend remains
authoritative). It lists, per broadcastable notification category, the Teams channel destinations that receive a
reference-only post, and adds or removes routes (`GET|POST /integrations/teams/channel-routes`,
`DELETE .../{id}`).

- Only destination keys are shown. Webhook URLs live in the deployment secret file
  `TEAMS_CHANNEL_DESTINATIONS_FILE` and never reach the API or the browser.
- The screen is reachable while the optional module `teams` is off so routes can be prepared; posts are created
  only while the module is on (module switch in `/admin/modules`).
- Adapter mode (`real`, `fake`, `not_configured`) comes from the API and is repeated on the Integrations page.

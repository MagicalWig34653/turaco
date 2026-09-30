# Employee Incident

## Goal
Employee reports a problem with almost no ITSM friction.

## Flow
1. Portal authenticates User transparently where possible.
2. Device recognition produces a candidate + confidence/method; employee may override or choose no specific device.
3. Portal checks active Major Incidents/known service problems and contextually relevant Knowledge. Employee can subscribe instead of creating a duplicate.
4. If submitted, create Incident with reported/affected User, Device/Service relationships and a Device Context Snapshot where available.
5. Rules route Team/category/priority using known context; employee does not choose assignment group/impact/urgency unless needed.
6. IT works Ticket; Tasks may be created for concrete follow-up. Knowledge/known errors/software state appear contextually.
7. Waiting/customer response and resolution notifications use central Notification service.
8. Ticket resolves/closes; reusable solution can produce a Knowledge draft suggestion.

## Required integration
Authorization, audit, search, timeline, notifications, relationships, device freshness, knowledge suggestions and known-incident suppression.

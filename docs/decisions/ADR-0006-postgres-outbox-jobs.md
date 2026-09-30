# ADR-0006: PostgreSQL Outbox and Job Queue First

**Status:** Accepted

Use transactional outbox and PostgreSQL-backed jobs initially. Do not require Kafka/RabbitMQ/NATS/Redis. Introduce an external broker only when measured throughput, isolation or delivery requirements justify it.

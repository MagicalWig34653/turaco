# ADR-0008: Separate Connector and Endpoint Agents

**Status:** Accepted

Connector Agent (internal-network bridge) and Endpoint Agent (device management) are separate binaries/trust boundaries. The Endpoint Agent's privilege profile must not silently expand the connector. Both use typed capabilities rather than implicit remote shell.

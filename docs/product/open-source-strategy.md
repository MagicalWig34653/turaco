# Open-Source Strategy

**Status:** Proposed

Turaco is intended to be open-source-first rather than a deliberately crippled community edition.

## Product model

The same core product should support:

- self-hosting at no software license fee under the eventual open-source license;
- official managed hosting;
- professional support for self-hosted customers;
- migration/implementation projects;
- custom integrations and consulting;
- dedicated hosting and higher-SLA service tiers.

## No artificial feature split

Security, SSO, audit, backup compatibility and other capabilities required to operate Turaco responsibly should not be withheld merely to force an enterprise upgrade.

Commercial value should come primarily from reliable operation, support, expertise, migration and convenience.

## Preferred license direction

The current preferred direction is **AGPL-3.0-or-later** because Turaco is a network-delivered application and the project should remain useful when forks are operated as services.

This is a product/legal decision, not merely an engineering choice. Before public distribution:

1. obtain appropriate legal review;
2. confirm third-party dependency/license compatibility;
3. decide whether contribution governance uses DCO, CLA or another mechanism;
4. commit the actual `LICENSE` file;
5. document trademark policy separately from source licensing.

Until then, this bootstrap repository intentionally contains no license grant.

## Hosting automation boundary

Everything required to self-host Turaco should be public with the product. Internal infrastructure used by the official hosting operator (customer provisioning, private credentials, billing automation, internal operations tooling) may remain separate because it is not required to use the product itself.

Endpoint/Connector agents that run inside customer environments should be open source alongside the platform to maximize inspectability and trust.

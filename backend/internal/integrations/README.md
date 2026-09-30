# Integration Adapters

External systems are adapters around internal public contracts. Planned adapters include LDAP/AD, Intune, Autotask, Teams and email.

Do not place vendor SDK types in domain packages. An integration may import a module's `public` contract, but modules must not import vendor adapters.

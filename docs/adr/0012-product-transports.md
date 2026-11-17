# Product control uses generated RPC contracts

The console uses generated Connect clients for product and administrative JSON
operations. Existing REST consumers remain supported while each operation moves
to shared application behavior and a generated contract. Moving clients before
sharing behavior would create independent authorization, validation and error
policies; removing live routes would break Studio and external integrations.

For each migration, retain the old HTTP route as a compatibility adapter with its
existing JSON/status shape. Map the generated method to the same permission and
resource scope, test positive and negative authorization through both transports,
and test the shared operation before switching the console. Generated outputs
must reproduce through the pinned generator. The route inventory in
[product transports](../product-transports.md) identifies consumers and boundaries.

Compatibility routes remain for at least 90 days and two minor releases after
their equivalent generated method ships. Removal additionally requires migrated
first-party consumers, updated API documentation and an announced removal
release. This is a minimum window, not an automatic deletion date.

HTTP remains appropriate for binary upload/download sessions, downloadable
Packwiz manifests/archives, OpenAPI documents, OIDC redirects/callbacks, health
and metrics exposition. Their control operations still use generated contracts;
content URLs and protocol integrations retain their documented HTTP semantics.

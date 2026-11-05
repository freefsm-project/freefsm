# Item synchronization

`GET /api/v1/sync/items` synchronizes active Item references independently of Job Context. Item View is required. Pricebook View independently enables SKU, sale price, tax treatment, and `capabilities.select`. Cost and maintenance operations are absent.

The shared snapshot protocol accepts `scope=all`, `page_size` (default 50, maximum 200), and either `page` or `cursor`. Unknown, repeated, empty, or incompatible parameters reject. Apply complete replacement records atomically before advancing a page; promote the completion cursor only after the last page. Tokens are opaque and belong to their projection, authenticated client, scope, and page size. Continuity is at least 30 days.

Each Item has one stable opaque `id` and its retained numeric `legacy_id`. Existing selection and commercial-line APIs use that numeric identity; synchronization does not replace installed Items or their provenance. Catalog price changes appear in deltas. They never reprice an already captured editable document line.

Archive, input authority loss, or unsafe continuity requires cache purge and a fresh snapshot. A retained Item selection must still be active and authorized when creation commits. Replace or remove rejected inputs only through explicitly reviewed new intent. A snapshot is not acceptance of a staged document.

The response and status contract is in the locally served OpenAPI 3.1 document (`ItemSyncEnvelope`). This endpoint does not add website offline support.

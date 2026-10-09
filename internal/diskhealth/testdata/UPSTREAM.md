# Public SMART Fixtures

Unmodified test inputs from [Scrutiny](https://github.com/AnalogJ/scrutiny),
commit `9fc491ced4a10cbcc63111de00f762c6a26b345b`,
`webapp/backend/pkg/models/testdata/`. MIT license retained in LICENSE.scrutiny.
Serial numbers and paths are public fixtures, not Atlas production assets.
No source code from Scrutiny is vendored; its tested field semantics and fixtures
inform the adapter, which uses the standard JSON decoder and exact integers.

| File | SHA256 |
| --- | --- |
| smart-nvme.json | cc5a9dc0c2977894bdbdfdae098c42940b9ebc2ce3d8d98f2f9b9aebe0f3c584 |
| smart-nvme-failed.json | e60928e55ba1d1fa65a5c402197d8b4f70cb2a611da4d2475e11e49f4bb33c8b |
| smart-ata.json | 2308313063db61d39f894a2f597f05c95449e665d5d680d9e189eae3135d42ba |

The fixture named failed is not an independent confirmed-failure label.
The ATA fixture is an unsupported-model counterexample, not ATA production support.
`input.json` supplies explicitly synthetic metadata for offline software testing.

Sources: [NVMe 2.0c SMART log, section 5.16.1.3](https://www.nvmexpress.org/wp-content/uploads/NVM-Express-Base-Specification-2.0c-2022.10.04-Ratified.pdf)
and [smartctl source and manual](https://github.com/smartmontools/smartmontools).

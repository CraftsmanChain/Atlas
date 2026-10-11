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
| smart-sata-ssd.json (upstream smart-ata-full.json) | 98612cb35e0829219497c2866d291e7d38cbda52f87558c0758ad14682ffc584 |
| smart-ata-unknown.json (upstream smart-ata-failed-scrutiny.json) | de9fda06b3ada0cc64656169331d9f759b4c4cc971386fee03ff9beba21bbbeb |

The fixture named failed is not an independent confirmed-failure label.
The WDC ATA fixture now exercises a limited HDD profile; the Samsung 860 EVO
fixture exercises a limited SSD profile. Samsung 840 remains an unmatched-model
counterexample. Profile matching is not production or all-firmware validation.
`input.json` supplies explicitly synthetic metadata for offline software testing.
Its historical `public-ata-unsupported` reference is retained for compatibility;
the reference is not the current assessment. `input-disk.json` uses current names
and covers NVMe, SATA HDD, SATA SSD and an unmatched model. Both manifests have
synthetic capture metadata, not real capture times. Normalized attribute 177 is
not a remaining-life percentage. ATA temperature is displayed but no unverified
alarm range is invented. Mutations in unit tests are synthetic safety tests.

Sources: [NVMe 2.0c SMART log, section 5.16.1.3](https://www.nvmexpress.org/wp-content/uploads/NVM-Express-Base-Specification-2.0c-2022.10.04-Ratified.pdf)
and [smartctl source and manual](https://github.com/smartmontools/smartmontools).

## EDAC raw-field fixtures

`edac-public.json` wraps four unmodified sysfs file contents from node_exporter
commit `1271bc244266457fd225dcef9b8a33641b582c64`,
[sys.ttar](https://github.com/prometheus/node_exporter/blob/1271bc244266457fd225dcef9b8a33641b582c64/collector/fixtures/sys.ttar).
Full upstream archive SHA256: `a72844f95f8578f901be125f07ee8d4e9feead937c8da936b540d7170ce10ba4`.
The four paths are `sys/devices/system/edac/mc/mc0/` followed by
`ce_count`, `ce_noinfo_count`, `ue_count`, and `ue_noinfo_count`.
Apache-2.0 license retained in LICENSE.node_exporter.
The wrapper adds only schema/controller identifiers and leaves missing driver,
kernel, mc_name and reset timer absent/empty. The inconsistent counters are
not repaired: the expected result is insufficient data, not healthy.

`edac-synthetic-*.json` and `input-memory.json` are explicitly synthetic
software fixtures, not raw captures from a production node or failure labels.
They exercise baseline, zero and new-UE paths in the Linux sysfs format.
Only controller-scope, boot-scoped identity is supported; no DIMM serial is inferred.
Sources: [Linux EDAC sysfs ABI](https://github.com/torvalds/linux/blob/master/Documentation/ABI/testing/sysfs-devices-edac)
and [node_exporter EDAC collector](https://github.com/prometheus/node_exporter/blob/1271bc244266457fd225dcef9b8a33641b582c64/collector/edac_linux.go).

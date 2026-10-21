"""
===========================================================================

check_identity_parity.py - do the client's and the server's tables agree on IDs

The client draws a monster, skill or item from the numeric ID it is sent, and
the server sends the ID its own table holds. When the two tables are built
from different imports of the optional Alexandria overlay, the same codename
can hold different IDs on each side and a Weneg is drawn as a serpent. This
joins the characterdata, skilldata and itemdata tables of two textdata folders
by codename and reports every ID that differs and every codename only one side
holds. It reads the files and changes nothing.

Run it after every import and server export:

	python scripts/analysis/check_identity_parity.py \
		--client <extracted>/Media_extracted/server_dep/silkroad/textdata \
		--server <server-game-data>/textdata

===========================================================================
"""
import argparse
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import import_vsro_alexandria as archive  # noqa: E402

# The codename column of each table; the numeric ID is column 1 in all three.
TABLE_KEYS = {"characterdata": 2, "skilldata": 3, "itemdata": 2}
ID_COLUMN = 1
EXAMPLE_LIMIT = 10


# ================
# read_identities
#
# {table: {codename: id}} of every shard file of the three tables. A shard
# index file (characterdata.txt lists shard names) has no ID column and is
# skipped by the same rule as any other short or non-numeric row.
# ================
def read_identities(textdata):
	result = {}
	for table, key in TABLE_KEYS.items():
		identities = {}
		for path in sorted(Path(textdata).glob(table + "*.txt")):
			for row in archive.table_rows(path.read_bytes()):
				if len(row) > key and row[ID_COLUMN].strip().isdigit():
					identities[row[key].strip()] = int(row[ID_COLUMN])
		result[table] = identities
	return result


# ================
# compare_identities
#
# The differences between two read_identities results, as a dict of lists.
# ================
def compare_identities(client, server):
	report = {}
	for table in TABLE_KEYS:
		left, right = client[table], server[table]
		report[table] = {
			"differing": sorted((code, left[code], right[code]) for code in left.keys() & right.keys() if left[code] != right[code]),
			"clientOnly": sorted(left.keys() - right.keys()),
			"serverOnly": sorted(right.keys() - left.keys()),
		}
	return report


# ================
# main
# ================
def main():
	parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
	parser.add_argument("--client", required=True, help="the client's textdata folder (the extraction the assets are built from)")
	parser.add_argument("--server", required=True, help="the exported server bundle's textdata folder")
	args = parser.parse_args()
	report = compare_identities(read_identities(args.client), read_identities(args.server))
	clean = True
	for table, found in report.items():
		counts = {name: len(rows) for name, rows in found.items()}
		clean = clean and not any(counts.values())
		print(f"{table}: {counts['differing']} differing IDs, {counts['clientOnly']} client only, {counts['serverOnly']} server only")
		for name, rows in found.items():
			for row in rows[:EXAMPLE_LIMIT]:
				print(f"  {name}: {json.dumps(row)}")
	if not clean:
		print("The client and server tables disagree: re-export the server data from the same extraction.")
	return 0 if clean else 1


if __name__ == "__main__":
	sys.exit(main())

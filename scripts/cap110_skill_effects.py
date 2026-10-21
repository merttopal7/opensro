"""
===========================================================================

cap110_skill_effects.py - optional animation sets for the imported skill tier

Port-only, not native. The later client authors animation sets for skill lines
the native v1.150 client never shipped. This merges those later skillaniset rows
into the native skilleffect.txt so the production effect-record build can emit a
record for every imported rank. Only an animation-set name the native table
lacks is inserted, inside its own section; a name both files carry keeps the
native row. No engine vocabulary is invented: a later row that names an unknown
native animation state is refused here, not guessed.

===========================================================================
"""
SECTION_HEADER = "#section"
ANIMSET_SECTIONS = ("skillaniset", "skillaniset2")
SERVICE_COLUMN = "skillaniset2"


# ================
# section_name
#
# The #section line's own name, lowercased, or None for a data row.
# ================
def section_name(line):
	if not line.startswith(SECTION_HEADER):
		return None
	columns = line.split("\t")
	return columns[1].strip().lower() if len(columns) > 1 else ""


# ================
# animset_base
#
# skillaniset2 prepends the loader-consumed Service column; skillaniset does
# not. The base name is the column after that.
# ================
def animset_base(section, columns):
	offset = 1 if section == SERVICE_COLUMN else 0
	if len(columns) <= offset + 1:
		return ""
	return columns[offset + 1].strip()


# ================
# animset_rows
#
# Yield (section, base, line) for every enabled animation-set row.
# ================
def animset_rows(text):
	section = None
	for line in text.splitlines():
		header = section_name(line)
		if header is not None:
			section = header
			continue
		if section not in ANIMSET_SECTIONS or not line.strip() or line.startswith("//"):
			continue
		columns = line.split("\t")
		if section == SERVICE_COLUMN and columns and columns[0].strip() == "0":
			continue
		base = animset_base(section, columns)
		if base:
			yield section, base, line


# ================
# native_bases
# ================
def native_bases(text):
	return {base for _section, base, _line in animset_rows(text)}


# ================
# remove_animation_bases
#
# Remove only this import's animation records. Other sections, including the
# Alexandria NPC characterInfo overlay, retain their authored rows.
# ================
def remove_animation_bases(text, bases):
	out, section = [], None
	for line in text.splitlines():
		header = section_name(line)
		if header is not None:
			section = header
		if section in ANIMSET_SECTIONS and not line.startswith((SECTION_HEADER, "//")):
			if animset_base(section, line.split("\t")) in bases:
				continue
		out.append(line)
	return "\r\n".join(out) + "\r\n"


# ================
# missing_animation_states
#
# Every ANI_* token a later row uses must exist in the native vocabulary; the
# build's animation registry drops unknown names, which would leave a silent
# record. Refuse the import instead of shipping one.
# ================
def missing_animation_states(later_lines, native_text):
	import re
	known = set(re.findall(r"ANI_[A-Z0-9_]+", native_text))
	unknown = set()
	for line in later_lines:
		for name in re.findall(r"ANI_[A-Z0-9_]+", line):
			if name not in known:
				unknown.add(name)
	return unknown


# ================
# select_later_rows
#
# The later rows whose base name is absent natively, grouped by section.
# ================
def select_later_rows(later_text, wanted):
	selected = {}
	for section, base, line in animset_rows(later_text):
		if base in wanted:
			selected.setdefault(section, []).append((base, line))
	for rows in selected.values():
		rows.sort()
	return selected


# ================
# merge_skill_effect_text
#
# Insert selected rows at the end of their own section, leaving every native
# byte otherwise intact. A later row whose base already appears natively is
# dropped (native wins).
# ================
def merge_skill_effect_text(native_text, selected):
	native_known = native_bases(native_text)
	pending = {
		section: [line for base, line in rows if base not in native_known]
		for section, rows in selected.items()
	}
	inserted = 0
	out, section = [], None
	# ================
	# flush
	# ================
	def flush():
		nonlocal inserted
		rows = pending.get(section, [])
		out.extend(rows)
		inserted += len(rows)
	for line in native_text.splitlines():
		header = section_name(line)
		if header is not None:
			flush()
			section = header
		out.append(line)
	flush()
	return "\r\n".join(out) + "\r\n", inserted

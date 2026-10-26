/*
===========================================================================

expanded-build.mjs - tells a test whether the build it reads has extra content

Census tests that pin the native counts (666 arrow stages, 27835 skills, ...)
are right for a native build and wrong for an expanded one (see
scripts/lib/expandedBuild.mjs). They pass NATIVE_ONLY and skip, with the
reason, instead of having their native numbers edited.

===========================================================================
*/
import fs from "node:fs";
import path from "node:path";
import { extractedRoot } from "../../../../scripts/build/world/paths.mjs";
import { EXPANDED_BUILD } from "../../../../scripts/lib/expandedBuild.mjs";

const SKILL_IMPORT_BUILD = fs.existsSync( path.join( extractedRoot, ".opensro-cap110-skills.json" ) );

export { EXPANDED_BUILD };
export const NATIVE_ONLY = {
	skip: EXPANDED_BUILD || SKILL_IMPORT_BUILD ?
		"native census: this build adds the Alexandria and cap-110 content" :
		false
};

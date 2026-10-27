/*
===========================================================================

archive-delivery.test.mjs - where the game files come from, and who gets
the desktop's read budgets

foundation/assets/archive-delivery.ts picks the delivery from the user
agent and the build's install gate. The desktop's larger read budgets
belong to the shell alone: the installed PWA runs on phones.

===========================================================================
*/
import "../helpers/native-source-loader.mjs";
import { pathToFileURL as sourceFileUrl } from "node:url";
import { test } from "node:test";
import assert from "node:assert/strict";
import path from "node:path";
import { root } from "../../tools/project.mjs";

const delivery = await import(
	sourceFileUrl( path.join( root, "src/engine/foundation/assets/archive-delivery.ts" ) ).href
);

const SHELL =
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 OpenSRODesktop/1.0";
const IPHONE =
	"Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1";

test("the shell, the installed app and a tab each get their delivery", () => {
	assert.equal( delivery.archiveDelivery( SHELL, true ), "shell" );
	assert.equal( delivery.archiveDelivery( SHELL, false ), "shell" );
	assert.equal( delivery.archiveDelivery( IPHONE, true ), "opfs" );
	assert.equal( delivery.archiveDelivery( IPHONE, false ), "network" );
});

test("only the shell gets the desktop's read budgets", () => {
	assert.equal( delivery.desktopReadBudgets( "shell" ), true );
	// An iPhone 13 running the installed PWA was killed at character start
	// under them; the web client's budgets play there.
	assert.equal( delivery.desktopReadBudgets( "opfs" ), false );
	assert.equal( delivery.desktopReadBudgets( "network" ), false );
});

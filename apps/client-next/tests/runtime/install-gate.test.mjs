/*
===========================================================================

install-gate.test.mjs - a gated build plays only from the installed app

foundation/install/install-gate.ts decides, from what a page can observe,
whether it is the desktop shell, an installed app or a tab, and which way a
tab can install. showInstallGate is checked on a minimal document: it
reveals the gate, and the install button follows Chrome's prompt events.
tools/pwa-manifest.mjs is checked for what an installable build publishes.

===========================================================================
*/
import "../helpers/native-source-loader.mjs";
import { pathToFileURL as sourceFileUrl } from "node:url";
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { root } from "../../tools/project.mjs";

const gate = await import( sourceFileUrl( path.join( root, "src/engine/foundation/install/install-gate.ts" ) ).href );
const pwa = await import( sourceFileUrl( path.join( root, "tools/pwa-manifest.mjs" ) ).href );

const UA = {
	chromeWindows:
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
	edgeWindows:
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0",
	chromeAndroid:
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36",
	desktopShell:
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 OpenSRODesktop/1.0",
	firefoxWindows: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0",
	iphoneSafari:
		"Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1",
	iphoneChrome:
		"Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/140.0.0.0 Mobile/15E148 Safari/604.1",
	// iPadOS asks for desktop sites: the same string as Safari on a Mac.
	macSafari:
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15",
	macChrome:
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
};

/*
================
env

An environment with WebGPU and OPFS in a tab, overridden per case.
================
*/
function env( overrides = {} ) {
	const modes = new Set( overrides.modes ?? [ "browser" ] );
	return {
		userAgent: UA.chromeWindows,
		maxTouchPoints: 0,
		standalone: false,
		hasWebGpu: true,
		hasOpfs: true,
		...overrides,
		displayMode: mode => modes.has( mode )
	};
}

test("the desktop shell and installed windows are not tabs", () => {
	assert.equal( gate.installMode( env( { userAgent: UA.desktopShell } ) ), "desktop-shell" );
	for ( const mode of [ "fullscreen", "standalone", "minimal-ui", "window-controls-overlay" ] ) {
		assert.equal( gate.installMode( env( { modes: [ mode ] } ) ), "installed", mode );
	}
	assert.equal( gate.installMode( env( { userAgent: UA.iphoneSafari, standalone: true } ) ), "installed" );
	assert.equal( gate.installMode( env() ), "browser" );
});

test("each browser gets the install route it supports", () => {
	const cases = [
		[ UA.chromeWindows, 0, "prompt" ],
		[ UA.edgeWindows, 0, "prompt" ],
		[ UA.chromeAndroid, 5, "prompt" ],
		[ UA.macChrome, 0, "prompt" ],
		[ UA.firefoxWindows, 0, "unsupported" ],
		[ UA.iphoneSafari, 5, "ios" ],
		[ UA.iphoneChrome, 5, "ios" ],
		[ UA.macSafari, 0, "mac-safari" ],
		[ UA.macSafari, 5, "ios" ]
	];
	for ( const [userAgent, maxTouchPoints, route] of cases ) {
		assert.equal(
			gate.installRoute( env( { userAgent, maxTouchPoints } ) ),
			route,
			`${userAgent} touch=${maxTouchPoints}`
		);
	}
});

test("a device that cannot run the client is told before it downloads anything", () => {
	assert.equal( gate.installRoute( env( { hasWebGpu: false } ) ), "no-webgpu" );
	assert.equal( gate.installRoute( env( { hasOpfs: false } ) ), "unsupported" );
	assert.equal(
		gate.installRoute( env( { userAgent: UA.iphoneSafari, maxTouchPoints: 5, hasWebGpu: false } ) ),
		"no-webgpu"
	);
});

/*
================
fakePage

The few DOM pieces showInstallGate touches: the gate, its button, the
loading shell and the window's event target.
================
*/
function fakePage( { withGate = true } = {} ) {
	const target = () => {
		const listeners = new Map();
		return {
			addEventListener: ( type, fn ) => listeners.set( type, fn ),
			fire: ( type, event = {} ) => listeners.get( type )?.( event )
		};
	};
	const button = target();
	const gateElement = { hidden: true, dataset: {}, querySelector: () => button };
	const loading = {
		attributes: {},
		setAttribute( name, value ) {
			this.attributes[name] = value;
		}
	};
	const doc = {
		getElementById: id =>
			id === "install-gate" ? (withGate ? gateElement : null) : id === "startup-loading" ? loading : null
	};
	return { doc, win: target(), gate: gateElement, button, loading };
}

test("the gate shows the route's panel and hides the loading shell", () => {
	const page = fakePage();
	assert.equal( gate.showInstallGate( page.doc, page.win, "ios" ), true );
	assert.equal( page.gate.hidden, false );
	assert.equal( page.gate.dataset.route, "ios" );
	assert.equal( page.loading.attributes["data-active"], "false" );
});

test("without gate markup the page is not gated, so the game can still start", () => {
	const page = fakePage( { withGate: false } );
	assert.equal( gate.showInstallGate( page.doc, page.win, "prompt" ), false );
});

test("the install button follows Chrome's prompt: offered, shown once, installed", () => {
	const page = fakePage();
	gate.showInstallGate( page.doc, page.win, "prompt" );
	assert.equal( page.gate.dataset.state, "waiting" );

	// A click before Chrome offers anything does nothing.
	page.button.fire( "click" );
	assert.equal( page.gate.dataset.state, "waiting" );

	let prevented = false, prompts = 0;
	page.win.fire( "beforeinstallprompt", {
		preventDefault: () => prevented = true,
		prompt: () => (prompts++, Promise.resolve())
	} );
	assert.equal( prevented, true );
	assert.equal( page.gate.dataset.state, "ready" );

	page.button.fire( "click" );
	page.button.fire( "click" );
	assert.equal( prompts, 1, "an offer prompts once" );
	assert.equal( page.gate.dataset.state, "waiting" );

	page.win.fire( "appinstalled" );
	assert.equal( page.gate.dataset.state, "installed" );
});

test("a build without the gate publishes no manifest", () => {
	const plugin = pwa.pwaManifest( pwa.pwaOptions( {} ) );
	assert.equal( plugin.generateBundle, undefined );
	assert.equal( plugin.transformIndexHtml, undefined );
});

test("a gated build needs a square icon of at least 512 px", () => {
	assert.throws( () => pwa.pwaManifest( pwa.pwaOptions( { SRO_INSTALL_GATE: "1" } ) ), /SRO_PWA_ICON/ );
	const dir = mkdtempSync( path.join( tmpdir(), "pwa-icon-" ) );
	try {
		const icon = ( width, height ) => {
			const bytes = Buffer.alloc( 33 );
			Buffer.from( [ 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a ] ).copy( bytes );
			bytes.writeUInt32BE( width, 16 );
			bytes.writeUInt32BE( height, 20 );
			const file = path.join( dir, `${width}x${height}.png` );
			writeFileSync( file, bytes );
			return file;
		};
		const options = file => pwa.pwaOptions( { SRO_INSTALL_GATE: "1", SRO_PWA_ICON: file } );
		assert.throws( () => pwa.pwaManifest( options( icon( 512, 256 ) ) ), /square/ );
		assert.throws( () => pwa.pwaManifest( options( icon( 256, 256 ) ) ), /512/ );

		// The files the browser loads by URL, from a stand-in published tree.
		const published = path.join( dir, "public" );
		mkdirSync( path.join( published, "assets", "cursors" ), { recursive: true } );
		writeFileSync( path.join( published, "assets", "cursors", "c.png" ), "cursor" );
		const native = { root: published, urls: [ "/assets/cursors/c.png" ] };

		const emitted = [];
		const plugin = pwa.pwaManifest( options( icon( 512, 512 ) ), native );
		plugin.generateBundle.call( { emitFile: file => emitted.push( file ) } );
		assert.equal( String( emitted.find( f => f.fileName === "assets/cursors/c.png" )?.source ), "cursor" );
		const missing = pwa.pwaManifest( options( icon( 512, 512 ) ), {
			root: published,
			urls: [ "/assets/gone.png" ]
		} );
		assert.throws( () => missing.generateBundle.call( { emitFile: () => {} } ), /assets\/gone\.png/ );
		const manifest = JSON.parse( emitted.find( f => f.fileName === "manifest.webmanifest" ).source );
		assert.equal( manifest.display, "fullscreen" );
		assert.deepEqual( manifest.icons[0], {
			src: "/pwa-icon.png",
			sizes: "512x512",
			type: "image/png",
			purpose: "any"
		} );
		assert.ok( emitted.some( f => f.fileName === "pwa-icon.png" ) );
		assert.deepEqual( plugin.transformIndexHtml( "", { path: "/index.html" } ), [
			{ tag: "link", attrs: { rel: "manifest", href: "/manifest.webmanifest" }, injectTo: "head" }
		] );
		assert.equal( plugin.transformIndexHtml( "<html>", { path: "/viewer.html" } ), "<html>" );
	} finally {
		rmSync( dir, { recursive: true, force: true } );
	}
});

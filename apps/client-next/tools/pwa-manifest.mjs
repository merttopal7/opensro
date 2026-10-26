/*
===========================================================================

pwa-manifest.mjs - the web app manifest of an installable build

Stage A2 of docs/PWA-OPFS-MIGRATION.md. A build with SRO_INSTALL_GATE=1
plays only from the installed app, so it must be installable: it links
/manifest.webmanifest from index.html and publishes the manifest and its
icon beside the page (served by the dev server, emitted by the build).

The icon is not in the repository. Game art is not covered by the source
license (NOTICE.md), so the build reads a square PNG from SRO_PWA_ICON (for
example the desktop shell's src-tauri/icons/icon.png) and refuses to build
an installable client without one. Builds without the gate are unchanged:
no manifest, no link.

A gated build also carries the few files the browser loads by URL
(foundation/assets/native-assets.ts: the boot screen's images and the
world cursors; stage A5). The installed app reads every other game file
from its archives, but these are drawn before any script runs, while the
first download is still going, and a web host that serves only the
client's own files has no other copy of them. They are copied from the
published asset tree, as the beta builder publishes them.

===========================================================================
*/
import { readFileSync } from "node:fs";
import path from "node:path";
import { CLIENT_PUBLIC_ROOT } from "../../../scripts/lib/generatedRoot.mjs";
import { nativeAssetUrls } from "../src/engine/foundation/assets/native-assets.ts";

const MANIFEST_PATH = "manifest.webmanifest";
const ICON_PATH = "pwa-icon.png";
const BACKGROUND = "#101820";
const PNG_SIGNATURE = [ 0x89, 0x50, 0x4e, 0x47 ];

/*
================
pwaOptions

The plugin's settings from the build environment (vite.config.mjs loadEnv).
================
*/
export function pwaOptions( env ) {
	return {
		enabled: env.SRO_INSTALL_GATE === "1",
		icon: env.SRO_PWA_ICON ?? "",
		name: env.SRO_PWA_NAME || "Silkroad Online",
		shortName: env.SRO_PWA_SHORT_NAME || "Silkroad",
		startUrl: env.SRO_PWA_START_URL || "/"
	};
}

/*
================
pngSize

Width and height from a PNG's IHDR chunk, which always follows the
8-byte signature.
================
*/
function pngSize( bytes ) {
	if ( !PNG_SIGNATURE.every( ( b, i ) => bytes[i] === b ) ) throw Error( "SRO_PWA_ICON is not a PNG" );
	return { width: bytes.readUInt32BE( 16 ), height: bytes.readUInt32BE( 20 ) };
}

/*
================
manifestFor

The manifest document. Fullscreen first, as the desktop shell runs; the
id pins the app's identity to the start URL so a later start_url change
does not create a second installed app.
================
*/
export function manifestFor( options, icon ) {
	return {
		id: options.startUrl,
		name: options.name,
		short_name: options.shortName,
		start_url: options.startUrl,
		scope: "/",
		display: "fullscreen",
		display_override: [ "fullscreen", "standalone" ],
		orientation: "landscape",
		background_color: BACKGROUND,
		theme_color: BACKGROUND,
		icons: [ { src: "/" + ICON_PATH, sizes: `${icon.width}x${icon.height}`, type: "image/png", purpose: "any" } ]
	};
}

/*
================
nativeFiles

The native assets' bytes by output file name, read from root. A gated
build without one of them would show a broken boot screen, so a missing
file fails the build.
================
*/
export function nativeFiles( root, urls ) {
	return urls.map( url => {
		const file = path.join( root, ...url.split( "/" ).filter( Boolean ) );
		let source;
		try {
			source = readFileSync( file );
		} catch {
			throw Error( `A gated build publishes ${url}, but ${file} is missing: build the client assets first` );
		}
		return { fileName: url.replace( /^\/+/, "" ), source };
	} );
}

/*
================
pwaManifest

The Vite plugin. Validates the icon when the config loads, so a gated build
without one fails before compiling anything. native names where the files
the browser loads by URL come from (the published asset tree by default).
================
*/
export function pwaManifest( options, native = { root: CLIENT_PUBLIC_ROOT, urls: nativeAssetUrls() } ) {
	if ( !options.enabled ) return { name: "sro-pwa-manifest" };
	if ( !options.icon ) throw Error( "SRO_INSTALL_GATE=1 needs SRO_PWA_ICON: a square PNG of at least 512x512" );
	const icon = readFileSync( options.icon );
	const size = pngSize( icon );
	if ( size.width !== size.height || size.width < 512 ) {
		throw Error( `SRO_PWA_ICON must be square and at least 512x512, not ${size.width}x${size.height}` );
	}
	const manifest = JSON.stringify( manifestFor( options, size ), null, "\t" ) + "\n";
	const files = new Map( [
		[ "/" + MANIFEST_PATH, { type: "application/manifest+json", body: manifest } ],
		[ "/" + ICON_PATH, { type: "image/png", body: icon } ]
	] );
	return {
		name: "sro-pwa-manifest",
		configureServer( server ) {
			server.middlewares.use( ( request, response, next ) => {
				const file = files.get( new URL( request.url, "http://localhost" ).pathname );
				if ( !file ) return next();
				response.setHeader( "Content-Type", file.type );
				response.end( file.body );
			} );
		},
		transformIndexHtml( html, context ) {
			// The game's page only; the embeddable viewer is not an app.
			if ( !context.path.endsWith( "/index.html" ) && context.path !== "/" ) return html;
			return [ { tag: "link", attrs: { rel: "manifest", href: "/" + MANIFEST_PATH }, injectTo: "head" } ];
		},
		generateBundle() {
			this.emitFile( { type: "asset", fileName: MANIFEST_PATH, source: manifest } );
			this.emitFile( { type: "asset", fileName: ICON_PATH, source: icon } );
			// The dev server already serves the whole published tree
			// (published-assets.mjs); only a build needs its own copies.
			for ( const file of nativeFiles( native.root, native.urls ) ) this.emitFile( { type: "asset", ...file } );
		}
	};
}

/*
===========================================================================

install-gate.ts - play only from the installed app

Stage A2 of docs/PWA-OPFS-MIGRATION.md. The installed PWA keeps the game's
.temir archives in the Origin Private File System. Two platform rules make a
browser tab the wrong place for them: on iOS a Home Screen app has its own
storage, apart from Safari's, so files fetched in a tab never reach the
installed app; and Safari erases a tab's script storage after seven days of
use without a visit, which an installed app is exempt from. So a build with
the gate (SRO_INSTALL_GATE=1, vite.config.mjs) starts the game only in the
desktop shell or the installed app, and a tab shows how to install instead.

The decisions are pure functions of what the page can observe
(InstallEnvironment); showInstallGate only reveals the gate's static markup
in index.html and offers Chrome's install prompt. Every user-facing word is
in that markup, none here. Port-only, not native: the original client has
no browser to gate.

===========================================================================
*/

/*
================
InstallEnvironment

What the page can observe about where it runs. displayMode answers a
display-mode media query; standalone is Safari's navigator.standalone.
================
*/
export interface InstallEnvironment {
	userAgent: string;
	maxTouchPoints: number;
	standalone: boolean;
	displayMode: ( mode: string ) => boolean;
	hasWebGpu: boolean;
	hasOpfs: boolean;
}

// desktop-shell: the Tauri client (its own user agent token).
// installed: a PWA window. browser: a tab.
export type InstallMode = "desktop-shell" | "installed" | "browser";

// What the gate offers a tab: Chrome/Edge's own install prompt, Safari's
// manual Add to Home Screen / Add to Dock, or nothing the game can use.
export type InstallRoute = "prompt" | "ios" | "mac-safari" | "unsupported" | "no-webgpu";

// Every display mode an installed app may open in (manifest display and
// display_override); a tab is always "browser".
const INSTALLED_DISPLAY_MODES = [ "fullscreen", "standalone", "minimal-ui", "window-controls-overlay" ] as const;

/*
================
installEnvironment

Reads the environment from the live page. Kept apart from the decisions so
they can be tested without one.
================
*/
export function installEnvironment( win: Window ): InstallEnvironment {
	const nav = win.navigator as Navigator & { standalone?: boolean; gpu?: unknown; };
	return {
		userAgent: nav.userAgent,
		maxTouchPoints: nav.maxTouchPoints ?? 0,
		standalone: nav.standalone === true,
		displayMode: mode => win.matchMedia( `(display-mode: ${mode})` ).matches,
		hasWebGpu: nav.gpu !== undefined,
		hasOpfs: typeof nav.storage?.getDirectory === "function"
	};
}

/*
================
installMode
================
*/
export function installMode( env: InstallEnvironment ): InstallMode {
	if ( /\bOpenSRODesktop\//.test( env.userAgent ) ) return "desktop-shell";
	if ( env.standalone || INSTALLED_DISPLAY_MODES.some( mode => env.displayMode( mode ) ) ) return "installed";
	return "browser";
}

/*
================
isAppleTouch

iPhone, iPod and iPad. iPadOS asks for desktop sites and reports itself as
a Mac; a touch screen tells them apart, since no Mac has one.
================
*/
function isAppleTouch( env: InstallEnvironment ) {
	return /\b(?:iPhone|iPad|iPod)\b/.test( env.userAgent ) ||
		/\bMacintosh\b/.test( env.userAgent ) && env.maxTouchPoints > 1;
}

/*
================
installRoute

Which way a tab can become the installed app. WebGPU and OPFS come first:
installing a client the device cannot run would only fail later, after a
multi-gigabyte download. Every iOS browser installs through its share menu.
Chromium browsers offer beforeinstallprompt; Safari on a Mac has Add to
Dock; Firefox on the desktop installs no web apps.
================
*/
export function installRoute( env: InstallEnvironment ): InstallRoute {
	if ( !env.hasWebGpu ) return "no-webgpu";
	if ( !env.hasOpfs ) return "unsupported";
	if ( isAppleTouch( env ) ) return "ios";
	const ua = env.userAgent;
	if ( /\bFirefox\//.test( ua ) ) return "unsupported";
	if ( /\b(?:Chrome|Chromium|Edg|OPR)\//.test( ua ) ) return "prompt";
	if ( /\bMacintosh\b/.test( ua ) && /\bSafari\//.test( ua ) ) return "mac-safari";
	return "unsupported";
}

/*
================
showInstallGate

Hides the loading shell and reveals #install-gate for route. The markup
selects the panel by data-route and the prompt button's text by
data-state: "waiting" until Chrome offers its prompt (it never does for an
app already installed, so that panel tells the player to open the app),
"ready" once it can be shown, "installed" after appinstalled. Returns false
when the page has no gate markup, so the caller can start the game instead
of leaving a blank page. The install events are Chromium's
(tools/types/install-prompt.d.ts).
================
*/
export function showInstallGate( doc: Document, win: Window, route: InstallRoute ) {
	const gate = doc.getElementById( "install-gate" );
	if ( !gate ) return false;
	doc.getElementById( "startup-loading" )?.setAttribute( "data-active", "false" );
	gate.dataset.route = route;
	gate.dataset.state = "waiting";
	gate.hidden = false;
	if ( route !== "prompt" ) return true;

	const button = gate.querySelector( "button[data-install]" );
	let offer: BeforeInstallPromptEvent | null = null;
	win.addEventListener( "beforeinstallprompt", event => {
		// Keep Chrome's own mini-infobar away; the gate's button shows it.
		event.preventDefault();
		offer = event;
		gate.dataset.state = "ready";
	} );
	win.addEventListener( "appinstalled", () => {
		offer = null;
		gate.dataset.state = "installed";
	} );
	button?.addEventListener( "click", () => {
		if ( !offer ) return;
		// The event can prompt once; Chrome fires a fresh one if declined.
		const shown = offer;
		offer = null;
		gate.dataset.state = "waiting";
		void shown.prompt();
	} );
	return true;
}

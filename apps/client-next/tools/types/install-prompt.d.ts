/*
===========================================================================

install-prompt.d.ts - Chromium's web app install events

beforeinstallprompt and appinstalled are not in TypeScript's DOM library
(they are Chromium's, from the WICG "Web App Install Prompt" draft). The
install gate (src/engine/foundation/install/install-gate.ts) uses them, and
declaring them here, as the browser API they are, keeps prompt() an
external boundary for the execution map rather than client code.

===========================================================================
*/

interface BeforeInstallPromptEvent extends Event {
	readonly platforms: readonly string[];
	readonly userChoice: Promise<{ outcome: "accepted" | "dismissed"; platform: string; }>;
	prompt(): Promise<{ outcome: "accepted" | "dismissed"; }>;
}

interface WindowEventMap {
	beforeinstallprompt: BeforeInstallPromptEvent;
	appinstalled: Event;
}

/*
===========================================================================

archive-delivery.ts - where this client's game files come from

Three deliveries share one client (docs/PWA-OPFS-MIGRATION.md, K5):

  network   the web client: packs over HTTP, each member verified by
            SHA-256 and kept in Cache Storage (packs/persistent.ts)
  shell     the desktop shell (new-silkroad): the same URLs answered from
            local .temir archives by its loopback edge
  opfs      the installed PWA of a gated build: the .temir archives
            downloaded into the Origin Private File System and read there

The shell and the PWA both read local archives: no Cache Storage, no
per-member SHA-256 (the archives were verified when they arrived), and the
desktop's larger read budgets. Only the PWA downloads and reads the
archives itself. A gated build (SRO_INSTALL_GATE=1) starts the game only
in the shell or the installed app, so outside the shell it is the PWA.
Port-only, not native.

===========================================================================
*/

export type ArchiveDelivery = "network" | "shell" | "opfs";

/*
================
installGateBuild

True in a build made with SRO_INSTALL_GATE=1 (vite.config.mjs defines the
value). Outside Vite, as in the Node test loader, import.meta.env does not
exist and the build is ungated.
================
*/
export function installGateBuild(): boolean {
	return typeof import.meta.env === "object" && import.meta.env.SRO_INSTALL_GATE === true;
}

/*
================
archiveDelivery
================
*/
export function archiveDelivery( userAgent: string, gated: boolean ): ArchiveDelivery {
	if ( /\bOpenSRODesktop\//.test( userAgent ) ) return "shell";
	return gated ? "opfs" : "network";
}

/*
================
currentArchiveDelivery

This page's or worker's delivery.
================
*/
export function currentArchiveDelivery(): ArchiveDelivery {
	const userAgent = typeof navigator === "undefined" ? "" : navigator.userAgent;
	return archiveDelivery( userAgent, installGateBuild() );
}

/*
================
updatesBaseUrl

Where patches.json and the archives are published: SRO_UPDATES_URL from the
build (an absolute URL, or a path on this origin), else /updates/ on this
origin, as the Agent and the edge serve it. Always ends with a slash.
================
*/
export function updatesBaseUrl( origin: string ): string {
	const configured = typeof import.meta.env === "object" ? import.meta.env.SRO_UPDATES_URL : undefined;
	const base = new URL( typeof configured === "string" && configured ? configured : "/updates/", origin ).href;
	return base.endsWith( "/" ) ? base : base + "/";
}

/*
===========================================================================

bear-roar.ts - port-only attack-pet roar on the retail bear skeleton

Authors a separate clip from the neutral stance; no attack motion is reused.
Counter-rotation at the hips keeps the hind legs planted while the torso rises.

===========================================================================
*/
import type { CharacterModel, CharacterChannel } from "@/engine/contracts/character";
import { multiplyQuaternion } from "@/engine/foundation/math/pose-math";

/*
================
withBearRoar

Called once at model admission. Original nodes, skins and clips remain intact.
The existing attack-pet server flag owns whether a roar emote can be emitted.
================
*/
export function withBearRoar( model: CharacterModel ): CharacterModel {
	const ROAR_TIMES = [ 0, .2, .65, .85, 1, 1.18, 1.38, 1.6, 1.85, 2.2 ];
	const RISE = [ 0, .08, .8, 1, 1, .96, 1, .9, .45, 0 ];
	const JAW = [ 0, 0, 4, 25, 36, 24, 34, 16, 0, 0 ];
	const DEGREES = Math.PI / 180;
	const stand = model.clips.find( clip => clip.name === "stand" );
	const byName = new Map( model.nodes.map( ( node, index ) => [ node.name, index ] ) );
	if ( !stand || model.clips.some( clip => clip.name === "pet-roar" ) ) return model;
	for ( const name of [ "Bip01 Pelvis", "Bip01 L Thigh", "Bip01 R Thigh", "Bip01 Head", "Bone01" ] ) {
		if ( !byName.has( name ) ) return model;
	}
	const translations = model.nodes.map( node => [ ...node.translation ] );
	const rotations = model.nodes.map( node => new Float32Array( node.rotation ) );
	for ( const channel of stand.channels ) {
		if ( channel.path === "translation" ) translations[channel.node] = [ ...channel.values.slice( 0, 3 ) ];
		if ( channel.path === "rotation" ) rotations[channel.node] = channel.values.slice( 0, 4 );
	}
	const channels: CharacterChannel[] = [];
	const keys = model.nodes.map( () => new Float32Array( ROAR_TIMES.length * 4 ) );
	const identity = new Float32Array( [ 0, 0, 0, 1 ] );
	for ( let frame = 0; frame < ROAR_TIMES.length; frame++ ) {
		const globals = new Map<number, Float32Array>();
		/*
		================
		poseBone

		Pitch in model space, then return to the parent's local space. The native
		biped uses different local axes for its torso and left/right limbs.
		================
		*/
		function poseBone( index: number ): Float32Array {
			const existing = globals.get( index );
			if ( existing ) return existing;
			const node = model.nodes[index]!;
			const parent = node.parent < 0 ? identity : poseBone( node.parent );
			const global = new Float32Array( 4 );
			multiplyQuaternion( parent, rotations[index]!, global );
			let pitch = 0;
			if ( node.name === "Bip01 Pelvis" ) pitch = -70 * RISE[frame]!;
			else if ( /Bip01 [LR] Thigh/.test( node.name ) ) pitch = 70 * RISE[frame]!;
			else if ( node.name === "Bip01 Neck" ) pitch = 35 * RISE[frame]!;
			else if ( node.name === "Bip01 Head" ) pitch = 20 * RISE[frame]!;
			else if ( /Bip01 [LR] UpperArm/.test( node.name ) ) pitch = -20 * RISE[frame]!;
			else if ( node.name === "Bone01" ) pitch = JAW[frame]!;
			const half = pitch * DEGREES / 2;
			const pitched = new Float32Array( 4 );
			multiplyQuaternion( [ Math.sin( half ), 0, 0, Math.cos( half ) ], global, pitched );
			const local = keys[index]!.subarray( frame * 4, frame * 4 + 4 );
			multiplyQuaternion( [ -parent[0]!, -parent[1]!, -parent[2]!, parent[3]! ], pitched, local );
			globals.set( index, pitched );
			return pitched;
		}
		for ( let index = 0; index < model.nodes.length; index++ ) poseBone( index );
	}
	for ( let index = 0; index < model.nodes.length; index++ ) {
		channels.push( {
			node: index,
			path: "rotation",
			interpolation: "LINEAR",
			times: new Float32Array( ROAR_TIMES ),
			values: keys[index]!
		}, {
			node: index,
			path: "translation",
			interpolation: "LINEAR",
			times: new Float32Array( ROAR_TIMES ),
			values: new Float32Array( ROAR_TIMES.flatMap( () => translations[index]! ) )
		} );
	}
	return { ...model, clips: [ ...model.clips, { name: "pet-roar", duration: 2.2, channels } ] };
}

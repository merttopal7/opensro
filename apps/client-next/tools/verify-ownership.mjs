import { project, imports, resolve, root, main, ts } from "./project.mjs";
import path from "node:path";
/*
================
readonlyData

Readonly literal tables contain no executable initializers or mutable references.
================
*/
function readonlyData( value ) {
	if ( !value ) return false;
	if ( ts.isAsExpression( value ) ) return value.type.getText() === "const" && readonlyData( value.expression );
	if ( ts.isArrayLiteralExpression( value ) ) return value.elements.every( readonlyData );
	if ( ts.isObjectLiteralExpression( value ) ) {
		return value.properties.every( p =>
			ts.isPropertyAssignment( p ) && !ts.isComputedPropertyName( p.name ) && readonlyData( p.initializer )
		);
	}
	return ts.isNumericLiteral( value ) || ts.isStringLiteralLike( value ) ||
		[ ts.SyntaxKind.TrueKeyword, ts.SyntaxKind.FalseKeyword, ts.SyntaxKind.NullKeyword ].includes( value.kind ) ||
		ts.isPrefixUnaryExpression( value ) &&
			[ ts.SyntaxKind.MinusToken, ts.SyntaxKind.PlusToken ].includes( value.operator ) &&
			ts.isNumericLiteral( value.operand );
}
export function verifyOwnership( base = root ) {
	let model;
	try {
		model = project( base );
	} catch ( error ) {
		return [ `Cannot read ownership project: ${error.message}` ];
	}
	const m = model.manifest, issues = [], seen = new Map();
	const record = value => value !== null && typeof value === "object" && !Array.isArray( value );
	const sourcePath = value =>
		typeof value === "string" && value.startsWith( "src/" ) && !value.includes( "\\" ) &&
		path.posix.normalize( value ) === value && model.files.has( value );
	if (
		!record( m ) || !sourcePath( m.root ) || !sourcePath( m.bootstrap ) ||
		![ "modules", "internals", "workers" ].every( key => record( m[key] ) ) ||
		m.contracts !== undefined && !record( m.contracts )
	) {
		return [ "Invalid ownership manifest schema or root endpoints" ];
	}
	m.contracts ??= {};
	for ( const key of [ "modules", "internals", "workers", "contracts" ] ) {
		for ( const [file, owner] of Object.entries( m[key] ) ) {
			if ( !sourcePath( file ) || !sourcePath( owner ) ) {
				issues.push( `Invalid ${key} endpoint: ${file} -> ${owner}` );
			}
		}
	}
	if ( issues.length ) return issues;
	if ( m.version !== 1 || m.root === m.bootstrap || m.modules[m.root] !== m.bootstrap ) {
		issues.push( "Invalid ownership root" );
	}
	const common = f => f.startsWith( "src/engine/contracts/" ) || f.startsWith( "src/engine/foundation/" );
	for ( const file of model.files.keys() ) {
		const classes = Number( file === m.bootstrap ) + Number( common( file ) ) +
			[ "modules", "internals", "contracts" ].filter( key => Object.hasOwn( m[key], file ) ).length;
		if ( classes > 1 ) issues.push( `Conflicting ownership classifications: ${file}` );
	}
	for ( const key of [ "internals", "contracts" ] ) {
		for ( const [file, owner] of Object.entries( m[key] ) ) {
			if ( !Object.hasOwn( m.modules, owner ) ) issues.push( `Undeclared ${key} owner: ${file} -> ${owner}` );
		}
	}
	for ( const [file, owner] of Object.entries( m.workers ) ) {
		if ( m.modules[file] !== owner ) issues.push( `Worker ownership mismatch: ${file}` );
	}
	for ( const [file, owner] of Object.entries( m.modules ) ) {
		if ( owner === m.bootstrap && file !== m.root ) issues.push( `Extra ownership root: ${file}` );
		if ( owner !== m.bootstrap && !m.modules[owner] ) {
			issues.push( `Undeclared owner: ${owner}` );
		}
		let p = file, ancestors = new Set();
		while ( m.modules[p] ) {
			if ( ancestors.has( p ) ) {
				issues.push( `Ownership cycle: ${file}` );
				break;
			}
			ancestors.add( p );
			p = m.modules[p];
		}
	}
	for ( const [file, source] of model.files ) {
		for ( const diagnostic of source.parseDiagnostics ) {
			issues.push( `${file}: ${ts.flattenDiagnosticMessageText( diagnostic.messageText, " " )}` );
		}
		if (
			file !== m.bootstrap && !common( file ) && !m.modules[file] && !m.internals[file] && !m.contracts?.[file]
		) {
			issues.push( `Unclassified source: ${file}` );
		}
		if (
			m.contracts?.[file] &&
			source.statements.some( s =>
				!ts.isImportDeclaration( s ) && !ts.isInterfaceDeclaration( s ) && !ts.isTypeAliasDeclaration( s )
			)
		) {
			issues.push( `Backend contracts must be type-only: ${file}` );
		}
		if ( common( file ) ) {
			for ( const statement of source.statements ) {
				if ( ts.isVariableStatement( statement ) ) {
					const immutable = Boolean( statement.declarationList.flags & ts.NodeFlags.Const );
					for ( const declaration of statement.declarationList.declarations ) {
						const value = declaration.initializer;
						const literal = value &&
							(ts.isNumericLiteral( value ) || ts.isBigIntLiteral( value ) ||
								ts.isStringLiteralLike( value ) ||
								[ ts.SyntaxKind.TrueKeyword, ts.SyntaxKind.FalseKeyword, ts.SyntaxKind.NullKeyword ]
									.includes( value.kind ) ||
								ts.isPrefixUnaryExpression( value ) &&
									(value.operator === ts.SyntaxKind.MinusToken ||
										value.operator === ts.SyntaxKind.PlusToken) &&
									ts.isNumericLiteral( value.operand ));
						const table = value && ts.isAsExpression( value ) && value.type.getText() === "const" &&
							readonlyData( value.expression );
						if ( !immutable || !(literal || table) ) {
							issues.push(
								`${file}: shared module state must be an immutable scalar constant or readonly literal table`
							);
						}
					}
				} else if (
					!ts.isImportDeclaration( statement ) && !ts.isExportDeclaration( statement ) &&
					!ts.isInterfaceDeclaration( statement ) && !ts.isTypeAliasDeclaration( statement ) &&
					!ts.isFunctionDeclaration( statement )
				) {
					issues.push( `${file}: shared module may contain only declarations and stateless functions` );
				}
			}
		}
		const scanned = imports( file, source );
		issues.push( ...scanned.errors );
		if ( m.contracts[file] && scanned.edges.some( edge => !edge.typeOnly ) ) {
			issues.push( `Backend contracts must use type-only dependencies: ${file}` );
		}
		for ( const edge of scanned.edges ) {
			const target = resolve( model, file, edge.specifier );
			if ( !target ) {
				issues.push( `${file}: unresolved or external dependency ${edge.specifier}` );
				continue;
			}
			const allowed = common( target ) ||
				m.modules[target] === file && (!m.workers[target] || edge.kind === "worker") ||
				m.internals[target] === file || m.internals[file] && m.internals[file] === m.internals[target] ||
				edge.typeOnly && m.contracts?.[target] &&
					file.startsWith( path.posix.dirname( m.contracts[target] ) + "/" );
			if ( common( file ) && !common( target ) || !allowed ) {
				issues.push( `${file}: forbidden ${edge.kind} of ${target}` );
			}
			if ( edge.kind === "worker" && m.workers[target] !== file ) {
				issues.push( `${file}: undeclared worker ${target}` );
			}
			if ( !edge.typeOnly ) {
				const key = file + ":" + target;
				seen.set( key, true );
			}
		}
	}
	for ( const [file, owner] of Object.entries( m.modules ) ) {
		if ( !seen.has( owner + ":" + file ) ) {
			issues.push( `Missing owner-to-child edge: ${owner} -> ${file}` );
		}
	}
	return issues;
}
if ( process.argv[1] === import.meta.filename ) {
	main( () => verifyOwnership() );
}

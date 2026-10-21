/*
===========================================================================

main.go - offline skill stream audit for cross-version data comparisons

Reads JSON arrays of raw skill cells, one per line, from stdin. Uses the
production compiler so parameter arguments cannot be mistaken for opcodes.
Successful decoding proves framing only, not execution support. Never loads
game data, starts services, or modifies the input archives.

===========================================================================
*/
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"opensro.online/server/internal/game/enterworld"
)

/*
================
auditResult
================
*/
type auditResult struct {
	Tags  []uint32 `json:"tags"`
	Error string   `json:"error,omitempty"`
}

/*
================
audit

An invalid stream is a row finding; invalid JSON is an invocation error.
================
*/
func audit(input io.Reader, output io.Writer) error {
	decoder := json.NewDecoder(input)
	encoder := json.NewEncoder(output)
	for {
		var fields []string
		if err := decoder.Decode(&fields); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		result := auditResult{Tags: []uint32{}}
		program, err := enterworld.CompileSkillProgram(fields)
		if err != nil {
			result.Error = err.Error()
		} else {
			for index := 0; index < program.Len(); index++ {
				result.Tags = append(result.Tags, program.Instruction(index).Tag)
			}
		}
		if err := encoder.Encode(result); err != nil {
			return err
		}
	}
}

/*
================
main
================
*/
func main() {
	if err := audit(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

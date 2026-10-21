/*
===========================================================================

main_test.go - audit protocol and production skill framing boundaries

Exercises arguments which equal tags, unknown blocks, and invalid input.

===========================================================================
*/
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

/*
================
TestAuditPreservesInstructionBoundaries
================
*/
func TestAuditPreservesInstructionBoundaries(t *testing.T) {
	fields := make([]string, 118)
	for index := range fields {
		fields[index] = "0"
	}
	// dura's argument spells att. It must remain an argument.
	fields[69], fields[70] = "1685418593", "6386804"
	var input, output bytes.Buffer
	if err := json.NewEncoder(&input).Encode(fields); err != nil {
		t.Fatal(err)
	}
	if err := audit(&input, &output); err != nil {
		t.Fatal(err)
	}
	var result auditResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || len(result.Tags) != 1 || result.Tags[0] != 0x64757261 {
		t.Fatalf("argument became an instruction: %+v", result)
	}
}

/*
================
TestAuditReportsUnknownInstructionAndContinues
================
*/
func TestAuditReportsUnknownInstructionAndContinues(t *testing.T) {
	fields := make([]string, 118)
	for index := range fields {
		fields[index] = "0"
	}
	fields[69] = "4294967295"
	var input, output bytes.Buffer
	encoder := json.NewEncoder(&input)
	if err := encoder.Encode(fields); err != nil {
		t.Fatal(err)
	}
	fields[69] = "0"
	if err := encoder.Encode(fields); err != nil {
		t.Fatal(err)
	}
	if err := audit(&input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var first, second auditResult
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Error, "unknown instruction ffffffff at 69") || second.Error != "" {
		t.Fatalf("unexpected audit results: %+v; %+v", first, second)
	}
}

/*
================
TestAuditRejectsInvalidJSON
================
*/
func TestAuditRejectsInvalidJSON(t *testing.T) {
	if err := audit(strings.NewReader("invalid"), &bytes.Buffer{}); err == nil {
		t.Fatal("invalid input was accepted")
	}
}

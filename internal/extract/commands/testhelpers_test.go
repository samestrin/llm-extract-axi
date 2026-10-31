package commands

import (
	"testing"

	"github.com/samestrin/llm-extract-axi/pkg/output"
)

// withAXI turns on TOON output for one test, as --axi does on the root.
//
// Ported from llm-data-axi's axi_coverage_test.go, which does not travel to
// this repo. TestExtractRelevantHonoursAXI would not compile without it.
func withAXI(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		output.SetDefaultAXI(false)
		GlobalAXIOutput = false
	})
	GlobalAXIOutput = true
	output.SetDefaultAXI(true)
}

package commands

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// openCodeCommandLine is the line in openCodePlugin that names the binary.
const openCodeCommandLine = `const command = "llm-extract"`

// openCodePlugin is the plugin for a binary on PATH, byte-equal to the
// committed integrations/opencode/llm-extract.js. It pins the OpenCode V2
// plugin API: https://opencode.ai/docs/build/plugins/server
const openCodePlugin = `// Managed by ` + "`llm-extract setup`" + `. Run setup again rather than editing this
// file; it rewrites the file when the binary moves.
//
// An OpenCode V2 server plugin, written against the plugin API at
// https://opencode.ai/docs/build/plugins/server
// It imports nothing from @opencode/plugin, so it loads without an install:
// V2 reads only the default export's id and setup().
//
// At the start of each session it runs llm-extract with no arguments (the
// home view) and adds the output to that session's system instructions. A
// failed run is reported to the session, never passed off as empty.
import { execFile } from "node:child_process"

` + openCodeCommandLine + `

function homeView(cwd) {
  return new Promise((resolve) => {
    execFile(command, [], { cwd, timeout: 10_000 }, (error, stdout, stderr) => {
      if (error) {
        const detail = [error.message, String(stdout), String(stderr)].map((s) => s.trim()).filter(Boolean)
        resolve(` + "`llm-extract failed at session start: ${detail.join(\"\\n\")}`" + `)
        return
      }
      resolve(String(stdout).trim())
    })
  })
}

export default {
  id: "llm-extract",
  async setup(ctx) {
    const views = new Map()
    await ctx.session.hook("context", async (event) => {
      if (!views.has(event.sessionID)) {
        views.set(event.sessionID, homeView(ctx.location.directory))
      }
      const text = await views.get(event.sessionID)
      if (text) {
        event.system.push({ type: "text", text })
      }
    })
  },
}
`

// planOpenCode is the OpenCode target: a plugin file in the project's
// .opencode/plugins/, which OpenCode V2 loads without any config entry
// (https://opencode.ai/docs/build/plugins#load). The whole file is ours, so
// a moved binary or a hand edit is repaired by rewriting it.
func planOpenCode(dir, bin string) ([]plannedWrite, error) {
	// bin goes into a JS string, which JSON quoting makes safe.
	lit, err := json.Marshal(bin)
	if err != nil {
		return nil, err
	}
	src := strings.Replace(openCodePlugin, openCodeCommandLine, "const command = "+string(lit), 1)
	p, err := planFile(filepath.Join(dir, ".opencode", "plugins", "llm-extract.js"), []byte(src))
	if err != nil {
		return nil, err
	}
	return []plannedWrite{p}, nil
}

// Managed by `llm-extract setup`. Run setup again rather than editing this
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

const command = "llm-extract"

function homeView(cwd) {
  return new Promise((resolve) => {
    execFile(command, [], { cwd, timeout: 10_000 }, (error, stdout, stderr) => {
      if (error) {
        const detail = [error.message, String(stdout), String(stderr)].map((s) => s.trim()).filter(Boolean)
        resolve(`llm-extract failed at session start: ${detail.join("\n")}`)
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

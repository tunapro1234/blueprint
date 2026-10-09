package identity

import "fmt"

// CompactionNoteMarker opens every compaction-survival note. A harness
// adapter (or a test) can use it to recognize bp's own text without matching
// the whole message, the same way the module journal uses a marker comment to
// recognize a file it wrote.
const CompactionNoteMarker = "[bp] Your context was compacted."

// CompactionNote is what an agent needs to keep working with bp after its
// context was compacted or cleared: it is still the named agent, its queued
// messages survived on disk, and here is how to keep talking to other agents.
// Every harness adapter that restates identity after a compaction (Claude's
// SessionStart hook, the OpenCode and Hermes compaction-hooks module) shares
// this one text, so the note an agent sees never depends on which harness it
// runs in.
func CompactionNote(agent string) string {
	return fmt.Sprintf(CompactionNoteMarker+" You are still the bp agent %q; your queued messages were kept. "+
		"Reply with `bp msg <agent> '<text>'`, see the other agents with `bp status`, and run `bp help` for more.", agent)
}

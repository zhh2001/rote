# vhs script for the rote dashboard demo.
# Run from the repository root: vhs demo.tape -> docs/demo.gif
# Requires Go, VHS, ttyd and ffmpeg. Builds rote from this checkout, not @latest.
# Preview without replacing the README GIF: vhs -o /tmp/rote-preview.gif demo.tape

Output docs/demo.gif

Require go
Require mktemp
Require clear

Set Shell "bash"
Set FontSize 16
Set Width 1100
Set Height 680
Set Theme "Catppuccin Mocha"
Set Padding 16
# Keep status/selection colors and English errors independent of the host shell.
Env TERM "xterm-256color"
Env COLORTERM "truecolor"
Env NO_COLOR ""
Env CLICOLOR "1"
Env CI ""
Env LC_ALL "C"

# Use a private directory for this recording's binary, database and lock file.
# Never remove a shared /tmp/rote-demo.db* or touch the user's default database.
Hide
Type@1ms `set -e; ROTE_DEMO_DIR=$(mktemp -d); readonly ROTE_DEMO_DIR; trap 'rm -rf -- "${ROTE_DEMO_DIR:?}"' EXIT`
Enter
Wait+Line /^> ?$/
Type@1ms `go build -o "$ROTE_DEMO_DIR/rote" ./cmd/rote`
Enter
# A cold build may take longer than a fixed Sleep; do not record a build error.
Wait+Line@120s /^> ?$/
Type@1ms `export PATH="$ROTE_DEMO_DIR:$PATH"; clear`
Enter
Wait+Line /^> ?$/
Show
Sleep 500ms

# Schedule the demo jobs and show the live dashboard.
Type `rote -c demo/jobs.toml --db "$ROTE_DEMO_DIR/rote.db"`
Enter
Wait+Screen /2 jobs/

# Confirm both outcomes, then let enough history accumulate to navigate it.
Wait+Screen /heartbeat.*✓/
Wait+Screen /disk-check.*✗/
Sleep 5s

# Move to the failing job and open its run history.
Down
Sleep 1s
Enter
Wait+Screen /job: disk-check/
Wait+Screen /No such file or directory/
Sleep 3s

# Step through earlier runs in the history; the output pane (its captured
# stderr) follows the selection.
Down
Sleep 2s
Down
Sleep 2s

# Switch focus to the output pane (this short error does not need scrolling).
Tab
Sleep 2s

# Quit gracefully and wait for the scheduler to stop before cleaning up.
Type "q"
Wait+Line /^> ?$/
Sleep 1s
Hide
Type@1ms "exit"
Enter
Sleep 500ms

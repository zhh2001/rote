# vhs script for the rote dashboard demo.
#   vhs demo.tape   ->   docs/demo.gif
# Requires `rote` on PATH (go install github.com/zhh2001/rote/cmd/rote@latest).

Output docs/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1100
Set Height 680
Set Theme "Catppuccin Mocha"
Set Padding 16

# Start from a clean database so the history grows on camera.
Hide
Type "rm -f /tmp/rote-demo.db*"
Enter
Show
Sleep 500ms

# Schedule the demo jobs and show the live dashboard.
Type "rote -c demo/jobs.toml --db /tmp/rote-demo.db"
Enter

# Let several runs happen: heartbeat succeeds (✓), disk-check fails (✗).
Sleep 8s

# Move to the failing job and open its run history.
Down
Sleep 1s
Enter
Sleep 3s

# Step through earlier runs in the history; the output pane (its captured
# stderr) follows the selection.
Down
Sleep 2s
Down
Sleep 2s

# Switch focus to the output pane to scroll the captured stderr.
Tab
Sleep 2s

# Quit: the TUI exits and stops the scheduler.
Type "q"
Sleep 1s

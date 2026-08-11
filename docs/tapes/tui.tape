# The TUI recording used in the README.
#
#   brew install vhs
#   vhs docs/tapes/tui.tape          # run from the repository root
#
# It records the real program against the real store, so it needs at least one
# installed VM to have anything to list — a running one is better, since the
# list is where march reports state. Nothing here creates or destroys a machine:
# the wizard is escaped out of before it submits, and q leaves running VMs
# running.

Output docs/images/tui.gif

Require march

Set Shell "bash"
Set FontFamily "Menlo"
Set FontSize 18
Set Width 1400
Set Height 600
Set Padding 30
Set Theme "TokyoNight"
Set TypingSpeed 60ms
Set Framerate 24

Hide
Type "clear" Enter
Sleep 1s
Show

Type "march"
Sleep 500ms
Enter
Sleep 6s
Escape
Sleep 2s

Enter
Sleep 3s
Escape
Sleep 1s

Type "y"
Sleep 3s
Escape
Sleep 1s

Type "n"
Sleep 2s
Type "demo"
Sleep 1s
Enter
Sleep 2s
Escape
Sleep 1s
Escape
Sleep 2s

Type "q"
Sleep 2s

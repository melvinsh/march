# The still of the list screen used in the README.
#
#   vhs docs/tapes/list.tape         # run from the repository root
#
# Separate from tui.tape only because the list is three lines tall and the GIF
# is sized for the details view; recording it on its own keeps the still tight.

Output "/tmp/march-list.gif"

Require march

Set Shell "bash"
Set FontFamily "Menlo"
Set FontSize 18
Set Width 1400
Set Height 300
Set Padding 30
Set Theme "TokyoNight"

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
Screenshot docs/images/tui.png

package docket

// KeysHelp lists every key of the docket view.
const KeysHelp = `In the list:
  j, k            move down or up one row; the right side shows it
  J, K            move down or up ten rows
  g, G            go to the first or last row
  Enter           show the row and move to the right side;
                  on an area, fold or unfold it
  Space           fold or unfold the area under the cursor
  /               find by name, title or bay across every level;
                  Enter keeps the filter, Esc clears it
  a               switch between front items and all levels
  click           go to the row; on an area, also fold or unfold it
  wheel           move down or up one row
  ?               show this list
  q               leave the view; it keeps running

From either side:
  Option-l, Option-h   move down or up one row
  Option-J, Option-K   the same
  Option-F             fold or unfold the area under the cursor
  Option-d             move between the list and the right side
  Option-D             go back to the tmux session you came from

The Option-l, -h, -J, -K and -F keys do nothing while a pane is zoomed.
Every other key, including the tmux prefix, goes to the window on the
right.`

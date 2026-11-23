package docket

// KeysHelp lists every key of the docket view.
const KeysHelp = `In the list:
  j, k            move down or up one row; the right side shows it
  J, K            move down or up ten rows
  g, G            go to the first or last row
  Enter           move to the right side
  Space           fold or unfold the area under the cursor
  /               find by name, title or bay across every level;
                  Enter keeps the filter, Esc clears it
  a               switch between front items and all levels
  click           go to the row; on an area, also fold or unfold it
  wheel           scroll the list; the cursor stays on its row
  ?               show this list
  q               leave the view; it keeps running

From either side:
  Option-J, Option-K   move down or up one row
  Option-Space         fold or unfold the area under the cursor
  Option-d             move between the list and the right side
  Option-D             leave the view, as q does

Option-J, Option-K and Option-Space do nothing while a pane is zoomed.
Every other key, including the tmux prefix, goes to the window on the
right.`

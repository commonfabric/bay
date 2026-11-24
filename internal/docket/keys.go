package docket

// KeysHelp lists every key of the docket view.
const KeysHelp = `In the list:
  j, k            move down or up one row; the right side shows it, and
                  for an area shows the first item in the area
  n, p            move to the next or previous item or bay, passing over
                  areas and the row of an item that lists its bays
  J, K            move down or up ten rows
  g, G            go to the first or last row
  Enter           move to the right side
  Space           fold or unfold the area under the cursor
  u               unfold every area; u again puts the folds back
  /               find by name, title or bay in the levels shown; an area
                  that matches lists all its items. The right side stays
                  as it is while you type. Enter keeps the filter and
                  shows the cursor's row. Esc clears the filter and
                  returns the cursor to the row the right side shows
  >, <            move the item under the cursor one level away from the
                  front, or one level toward it
  a               show more levels: front, then front + back, then
                  front + back + deep; the line above the key
                  reminder says which
  s               show the item's state, questions and what it waits on;
                  q, Esc or Enter closes it
  b               collapse the list to a narrow strip
  =               give the list its first width again
  click           go to the row; on an area, also fold or unfold it;
                  on « at the bottom, collapse the list; on » in
                  the same place, expand it
  drag            on the line between the panes, change the list's
                  width; a double click there gives the first width
  wheel           scroll the list; the cursor stays on its row
  ?               show this list
  q               leave the view; it keeps running

From either side:
  Option-J, Option-K   move down or up one row
  Option-N, Option-P   move to the next or previous item or bay, as n
                       and p do
  Option-Space         fold or unfold the area under the cursor
  Option-?             show the item's state, as s does
  Option-B             collapse or expand the list
  Option-d             move between the list and the right side
  Option-D             leave the view, as q does

Option-J, -K, -N, -P and Option-Space work while the list is collapsed, and
the row's name is then shown for a moment. They do nothing while a pane is
zoomed.
Every other key, including the tmux prefix, goes to the window on the
right.`

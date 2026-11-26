package docket

// KeysHelp lists every key of the docket view.
const KeysHelp = `In the list:
  j, k            move down or up one row; the right side shows it, and
                  for an area shows the area's own bay, or else the
                  first item in the area
  n, p            move to the next or previous item or bay, passing over
                  areas and the row of an item that lists its bays
  J, K            move down or up ten rows
  r               move to the next row with the waiting mark, from the
                  last row to the first
  g, G            go to the first or last row
  Enter           move to the right side, also from an area
  Space           fold or unfold the area under the cursor
  u               unfold every area; u again puts the folds back
  /               find by name, title or bay in the levels shown; an area
                  that matches lists all its items. The right side stays
                  as it is while you type. Enter keeps the filter and
                  shows the cursor's row. Esc clears the filter and
                  returns the cursor to the row the right side shows
  *               add the row under the cursor to the end of the
                  hotlist, the rows listed below the desk, or take it
                  off. At most nine. The row stays in its area, which
                  shows its number as *1. The cursor stays where it is;
                  after taking off the row it is on in the hotlist, it
                  goes to the row that takes its place, or the one above
                  when it was the last, or to the desk
  1 to 9          go to that row of the hotlist
  o               on a row of the hotlist, go to the same row in its
                  area, brought into the list as ' does, with the area
                  itself unfolded so that its items are listed below.
                  On a row in an area that is in the hotlist, go to its
                  row in the hotlist. '' goes back
  [, ]            in the hotlist, move the row up or down. Elsewhere,
                  move the item up or down among the shown items under
                  the same parent, with docket set --before or --after
  m, then a letter   mark the row under the cursor with that letter; on
                  the row that has the mark, take it off. m and a digit
                  is refused: only * changes the hotlist
  ', then a letter   go to the row with that mark. A row that is not in
                  the list is brought in: its areas are unfolded, its
                  level is shown, and the filter is cleared, as needed
  ', then a digit    go to that row of the hotlist, as the digit does
  ''              go back to the row the last ' left
                  A mark is forgotten when its item is closed or deleted.
  >, <            move the item under the cursor one level away from the
                  front, or one level toward it
  x               close the item or area under the cursor: then f for
                  finished, or d and a reason for dropped. Not done
                  while open items are under it, or while it lists
                  more than one bay. On a bay's row, close the bay
                  (y confirms); a bay with uncommitted changes is
                  refused. A bay that no longer exists is taken off
                  its item. An area with no open items is listed so
                  that it can be closed
  X               on a bay's row, close the bay even with
                  uncommitted changes, which are lost (y confirms)
  a               show more levels: front, then front + back, then
                  front + back + shelved; the line above the key
                  reminder says which
  s               show the item's state, questions and what it waits on;
                  q, Esc or Enter closes it
  B               show a row for every bay of every item and area,
                  where x closes the bay; B again shows them only for
                  items with more than one bay
  b               collapse the list to a narrow strip
  =               give the list its first width again
  click           go to the row; a click on the area the cursor is
                  already on folds or unfolds it;
                  on « at the bottom, collapse the list. A click
                  anywhere on the collapsed list expands it
  drag            on the line between the panes, change the list's
                  width; a double click there gives the first width.
                  The width is kept for the next start of the view
  wheel           scroll the list; the cursor stays on its row
  ?               show this list
  q               leave the view; it keeps running

From either side:
  Option-j, Option-k   in the list, move down or up one row; in the right
                       side, move between the panes of a bay's window
  Option-J, Option-K   the same as Option-j and Option-k
  Option-r             move to the next row with the waiting mark
  Option-g             move to the list and start a search, as / does
  Bay's other Option keys (Option-s, -a, -o and so on) act on the bay
  shown on the right.
  Option-N, Option-P   move to the next or previous item or bay, as n
                       and p do
  Option-Space         fold or unfold the area under the cursor
  Option-?             show the item's state, as s does
  Option-B             collapse or expand the list
  Option-', a letter   go to a mark, or with a digit to that row of the
                       hotlist; Option-' then ' goes back
  Option-M, a letter   mark the row the right side shows
  Option-1 to Option-9 go to that row of the hotlist
  Option-h, Option-l   move to the list, or to the right side
  Option-d             move between the list and the right side
  Option-D             leave the view, as q does

Option-r, -N, -P, Option-Space and the mark keys work while
the list is collapsed or the right side is zoomed, and the row's name is
then shown for a moment.
Every other key, including the tmux prefix, goes to the window on the
right.`

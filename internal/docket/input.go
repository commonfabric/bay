package docket

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParseKeys turns bytes read from the terminal into key names, and returns
// the bytes at the end that are the start of a key whose remainder has not
// arrived. The caller puts those bytes in front of its next read.
//
// A printable character is named by itself. The other names are "enter",
// "esc", "backspace", "up", "down" and "ctrl-c". A press of the left mouse
// button is "click:" and its line, counted from 0. The wheel is "wheelup"
// or "wheeldown". Every other key and mouse report produces no name.
func ParseKeys(input string) (keys []string, rest string) {
	if input == "\x1b" {
		// Nothing came with it, so the Escape key was pressed.
		return []string{"esc"}, ""
	}
	for input != "" {
		switch c := input[0]; {
		case c == '\x1b':
			name, used, complete := parseEscape(input)
			if !complete {
				return keys, input
			}
			if name != "" {
				keys = append(keys, name)
			}
			input = input[used:]
			continue
		case c == '\r' || c == '\n':
			keys = append(keys, "enter")
		case c == '\x7f' || c == '\b':
			keys = append(keys, "backspace")
		case c == '\x03':
			keys = append(keys, "ctrl-c")
		case c < ' ':
		default:
			if !utf8.FullRuneInString(input) {
				return keys, input
			}
			r, size := utf8.DecodeRuneInString(input)
			if r != utf8.RuneError {
				keys = append(keys, string(r))
			}
			input = input[size:]
			continue
		}
		input = input[1:]
	}
	return keys, ""
}

// parseEscape reads the key at the start of input, which begins with the
// escape character. It returns the key's name, or "" for a key that has
// none, and how many bytes the key takes. complete is false when the key's
// last bytes have not arrived.
func parseEscape(input string) (name string, used int, complete bool) {
	if len(input) == 1 {
		return "", 0, false
	}
	switch input[1] {
	case 'O':
		if len(input) < 3 {
			return "", 0, false
		}
		return arrow(input[2]), 3, true
	case '[':
		// The sequence ends at the first byte from '@' to '~'.
		for k := 2; k < len(input); k++ {
			if input[k] >= '@' && input[k] <= '~' {
				body, final := input[2:k], input[k]
				if strings.HasPrefix(body, "<") {
					return mouse(body[1:], final), k + 1, true
				}
				if body == "" {
					return arrow(final), k + 1, true
				}
				return "", k + 1, true
			}
		}
		return "", 0, false
	}
	// Escape followed by another character is that key with Option held.
	_, size := utf8.DecodeRuneInString(input[1:])
	return "", 1 + size, true
}

func arrow(final byte) string {
	switch final {
	case 'A':
		return "up"
	case 'B':
		return "down"
	}
	return ""
}

// mouse names a mouse report from its fields "button;column;line" and its
// last byte, which is 'M' for a press and 'm' for a release.
func mouse(fields string, final byte) string {
	parts := strings.Split(fields, ";")
	if final != 'M' || len(parts) != 3 {
		return ""
	}
	line, err := strconv.Atoi(parts[2])
	if err != nil {
		return ""
	}
	switch parts[0] {
	case "0":
		return fmt.Sprintf("click:%d", line-1)
	case "64":
		return "wheelup"
	case "65":
		return "wheeldown"
	}
	return ""
}

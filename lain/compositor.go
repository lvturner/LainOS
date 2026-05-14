package main

import (
	"strings"
	"unicode/utf8"

	ansi "github.com/charmbracelet/x/ansi"
)

func visualByteOffset(s string, targetCol int) int {
	byteIdx := 0
	visualCol := 0
	for byteIdx < len(s) {
		if s[byteIdx] == '\x1b' {
			end := ansiEscapeEnd(s, byteIdx)
			byteIdx = end
			continue
		}
		if visualCol >= targetCol {
			return byteIdx
		}
		_, rw := utf8.DecodeRuneInString(s[byteIdx:])
		r := s[byteIdx : byteIdx+rw]
		visualCol += ansi.StringWidth(r)
		byteIdx += rw
	}
	return byteIdx
}

func ansiEscapeEnd(s string, start int) int {
	i := start + 1
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		i++
		for i < len(s) {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
			i++
		}
		return i
	case ']':
		i++
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return i
	default:
		return i + 1
	}
}

func overlayLines(baseLines []string, overlayLines []string, x int, y int, termWidth int) []string {
	if len(baseLines) == 0 {
		return baseLines
	}

	result := make([]string, len(baseLines))
	copy(result, baseLines)

	for i, overlayLine := range overlayLines {
		targetRow := y + i
		if targetRow < 0 || targetRow >= len(result) {
			continue
		}

		baseLine := result[targetRow]
		baseVisualWidth := ansi.StringWidth(baseLine)

		if baseVisualWidth < termWidth {
			baseLine += strings.Repeat(" ", termWidth-baseVisualWidth)
		}

		leftOffset := visualByteOffset(baseLine, x)
		left := baseLine[:leftOffset]

		overlayVisualWidth := ansi.StringWidth(overlayLine)
		rightStart := x + overlayVisualWidth
		rightOffset := visualByteOffset(baseLine, rightStart)

		var right string
		if rightOffset <= len(baseLine) {
			right = baseLine[rightOffset:]
		}

		paddedOverlay := overlayLine
		if overlayVisualWidth < rightStart-x {
			padNeeded := rightStart - x - overlayVisualWidth
			paddedOverlay += strings.Repeat(" ", padNeeded)
		}

		result[targetRow] = left + paddedOverlay + right
	}

	return result
}

package main

import (
	"strings"

	ansi "github.com/charmbracelet/x/ansi"
)

func visualByteOffset(s string, targetCol int) int {
	byteIdx := 0
	visualCol := 0
	runes := []rune(s)
	for byteIdx < len(s) {
		if s[byteIdx] == '\x1b' {
			end := strings.IndexByte(s[byteIdx:], 'm')
			if end != -1 {
				end += byteIdx + 1
				byteIdx = end
				continue
			}
		}
		if visualCol >= targetCol {
			return byteIdx
		}
		r := runes[0]
		runes = runes[1:]
		rWidth := ansi.StringWidth(string(r))
		visualCol += rWidth
		byteIdx += len(string(r))
	}
	return byteIdx
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

		result[targetRow] = left + overlayLine + right
	}

	return result
}

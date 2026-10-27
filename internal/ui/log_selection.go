package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type logPoint struct{ x, y int }
type logArea struct {
	x, y, width int
	height      int
	lines       []string
	allLines    []string
	start       int
}

type logSelection struct {
	origin                logPoint
	width                 int
	lines                 []string
	start, end            logPoint
	dragging              bool
	viewStart, viewHeight int
	direction             int
	tickPending           bool
	pointer               logPoint
}

func (s logSelection) bounds() (logPoint, logPoint) {
	a, b := s.start, s.end
	if a.y > b.y || (a.y == b.y && a.x > b.x) {
		a, b = b, a
	}
	return a, b
}

func (s logSelection) text() string {
	if len(s.lines) == 0 || s.start == s.end {
		return ""
	}
	a, b := s.bounds()
	var lines []string
	for y := a.y; y <= b.y; y++ {
		left, right := 0, ansi.StringWidth(s.lines[y])
		if y == a.y {
			left = a.x
		}
		if y == b.y {
			right = b.x + 1
		}
		lines = append(lines, strings.TrimRight(ansi.Cut(s.lines[y], left, right), " "))
	}
	return strings.Join(lines, "\n")
}

func (m *model) startLogSelection(msg tea.MouseClickMsg) bool {
	if m.tab != tabInfra || m.disconnected || msg.Button != tea.MouseLeft {
		return false
	}
	area := m.logArea
	if len(area.lines) == 0 || msg.X < area.x || msg.X >= area.x+area.width || msg.Y < area.y || msg.Y >= area.y+len(area.lines) {
		return false
	}
	m.selectionGeneration++
	p := logPoint{msg.X - area.x, area.start + msg.Y - area.y}
	m.logSelection = logSelection{
		lines: append([]string(nil), area.allLines...), start: p, end: p, dragging: true,
		viewStart: area.start, viewHeight: len(area.lines), pointer: logPoint{msg.X, msg.Y},
		origin: logPoint{area.x, area.y}, width: area.width,
	}
	return true
}

func (m *model) moveLogSelection(x, y int) {
	s := &m.logSelection
	if len(s.lines) == 0 {
		return
	}
	s.pointer = logPoint{x, y}
	s.direction = 0
	if y < s.origin.y || (y == s.origin.y && s.start.y > s.viewStart) {
		s.direction = -1
	}
	if y >= s.origin.y+s.viewHeight || (y == s.origin.y+s.viewHeight-1 && s.start.y < s.viewStart+s.viewHeight-1) {
		s.direction = 1
	}
	if x < s.origin.x || x >= s.origin.x+s.width {
		s.direction = 0
	}
	s.end = logPoint{max(0, min(x-s.origin.x, s.width-1)), s.viewStart + max(0, min(y-s.origin.y, s.viewHeight-1))}
	if y < s.origin.y {
		s.end.x = 0
	}
	if y > s.origin.y+s.viewHeight-1 {
		s.end.x = s.width - 1
	}
}

func (m *model) renderLogSelection() string {
	s := m.logSelection
	a, b := s.bounds()
	start, end := s.viewStart, min(len(s.lines), s.viewStart+s.viewHeight)
	if s.viewHeight == 0 {
		start, end = 0, len(s.lines)
	}
	lines := append([]string(nil), s.lines[start:end]...)
	style := lipgloss.NewStyle().Reverse(true)
	if a != b {
		for y := max(a.y, start); y <= min(b.y, end-1); y++ {
			i := y - start
			left, right := 0, ansi.StringWidth(lines[i])
			if y == a.y {
				left = a.x
			}
			if y == b.y {
				right = b.x + 1
			}
			lines[i] = ansi.Cut(lines[i], 0, left) + style.Render(ansi.Cut(lines[i], left, right)) + ansi.Cut(lines[i], right, ansi.StringWidth(lines[i]))
		}
	}
	return strings.Join(lines, "\n")
}

type logSelectionScrollTick uint64

func (m *model) selectionScrollCmd() tea.Cmd {
	s := &m.logSelection
	if !s.dragging || s.tickPending || s.direction == 0 {
		return nil
	}
	if (s.direction < 0 && s.viewStart == 0) || (s.direction > 0 && s.viewStart+s.viewHeight >= len(s.lines)) {
		return nil
	}
	s.tickPending = true
	generation := m.selectionGeneration
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return logSelectionScrollTick(generation) })
}

func (m *model) scrollLogSelection(generation uint64) tea.Cmd {
	s := &m.logSelection
	if generation != m.selectionGeneration || !s.dragging || !s.tickPending || m.tab != tabInfra || m.disconnected {
		return nil
	}
	s.tickPending = false
	if s.direction == 0 {
		return nil
	}
	distance := max(0, s.origin.y-s.pointer.y, s.pointer.y-(s.origin.y+s.viewHeight-1))
	step := min(12, 3+distance)
	previous := s.viewStart
	s.viewStart = max(0, min(s.viewStart+s.direction*step, len(s.lines)-s.viewHeight))
	if s.viewStart == previous {
		s.direction = 0
		return nil
	}
	m.logsOffset = max(0, len(s.lines)-s.viewStart-s.viewHeight)
	m.moveLogSelection(s.pointer.x, s.pointer.y)
	if (s.direction < 0 && s.viewStart == 0) || (s.direction > 0 && s.viewStart+s.viewHeight == len(s.lines)) {
		s.direction = 0
	}
	return m.selectionScrollCmd()
}

func (m *model) finishLogSelection(x, y int) tea.Cmd {
	m.moveLogSelection(x, y)
	m.logSelection.dragging = false
	m.logSelection.direction = 0
	m.logSelection.tickPending = false
	if text := m.logSelection.text(); strings.TrimSpace(text) != "" {
		m.copiedNotice = true
		m.copiedGeneration++
		generation := m.copiedGeneration
		return tea.Batch(tea.SetClipboard(text), tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return copiedNoticeExpired(generation) }))
	}
	m.logSelection = logSelection{}
	return nil
}

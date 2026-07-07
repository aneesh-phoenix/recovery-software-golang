// Package tui provides terminal UI components with arrow-key navigation.
// No external dependencies — uses ANSI escape codes and raw terminal mode.
package tui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ANSI escape sequences
const (
	clearLine   = "\033[2K"
	moveUp      = "\033[%dA"
	moveDown    = "\033[%dB"
	moveToCol   = "\033[0G"
	hideCursor  = "\033[?25l"
	showCursor  = "\033[?25h"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorDim    = "\033[2m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

// MenuItem represents a single selectable option.
type MenuItem struct {
	Label       string // Display text
	Description string // Optional description shown on the right
	Value       string // Return value when selected
	Disabled    bool   // Greyed out and non-selectable
}

// SelectOption displays an interactive menu and returns the selected item index.
// Uses arrow keys (↑/↓) to navigate and Enter to confirm.
func SelectOption(title string, items []MenuItem) (int, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("no items to select from")
	}

	// Put terminal in raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return -1, fmt.Errorf("failed to set raw terminal mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	selected := 0
	// Find first non-disabled item
	for selected < len(items) && items[selected].Disabled {
		selected++
	}
	if selected >= len(items) {
		selected = 0
	}

	// Hide cursor during selection
	fmt.Print(hideCursor)
	defer fmt.Print(showCursor)

	// Print title
	fmt.Printf("\r\n  %s%s%s\r\n\r\n", colorBold, title, colorReset)

	// Initial render
	renderMenu(items, selected)

	// Input loop
	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return -1, err
		}

		switch {
		case n == 1 && buf[0] == 13: // Enter
			// Move cursor below the menu
			fmt.Printf("\r\n")
			return selected, nil

		case n == 1 && buf[0] == 3: // Ctrl+C
			fmt.Printf("\r\n")
			return -1, fmt.Errorf("cancelled")

		case n == 1 && buf[0] == 'q': // q to quit
			fmt.Printf("\r\n")
			return -1, fmt.Errorf("cancelled")

		case n == 3 && buf[0] == 27 && buf[1] == 91 && buf[2] == 65: // Up arrow
			prev := selected
			selected--
			for selected >= 0 && items[selected].Disabled {
				selected--
			}
			if selected < 0 {
				selected = prev // Don't wrap, stay in place
			}

		case n == 3 && buf[0] == 27 && buf[1] == 91 && buf[2] == 66: // Down arrow
			prev := selected
			selected++
			for selected < len(items) && items[selected].Disabled {
				selected++
			}
			if selected >= len(items) {
				selected = prev // Don't wrap, stay in place
			}

		case n == 1 && buf[0] == 'k': // vim up
			prev := selected
			selected--
			for selected >= 0 && items[selected].Disabled {
				selected--
			}
			if selected < 0 {
				selected = prev
			}

		case n == 1 && buf[0] == 'j': // vim down
			prev := selected
			selected++
			for selected < len(items) && items[selected].Disabled {
				selected++
			}
			if selected >= len(items) {
				selected = prev
			}
		}

		// Move cursor up to redraw
		fmt.Printf(moveToCol)
		fmt.Printf(moveUp, len(items))
		renderMenu(items, selected)
	}
}

// renderMenu draws the menu items with the current selection highlighted.
func renderMenu(items []MenuItem, selected int) {
	for i, item := range items {
		fmt.Print(clearLine)
		if item.Disabled {
			fmt.Printf("\r    %s%s%s", colorDim, item.Label, colorReset)
			if item.Description != "" {
				fmt.Printf("  %s%s%s", colorDim, item.Description, colorReset)
			}
		} else if i == selected {
			fmt.Printf("\r  %s❯ %s%s%s", colorCyan, colorBold, item.Label, colorReset)
			if item.Description != "" {
				fmt.Printf("  %s%s%s", colorDim, item.Description, colorReset)
			}
		} else {
			fmt.Printf("\r    %s", item.Label)
			if item.Description != "" {
				fmt.Printf("  %s%s%s", colorDim, item.Description, colorReset)
			}
		}
		fmt.Print("\r\n")
	}
}

// Confirm shows a yes/no prompt navigable with arrow keys.
// Returns true for yes, false for no.
func Confirm(prompt string, defaultYes bool) (bool, error) {
	items := []MenuItem{
		{Label: "Yes", Value: "yes"},
		{Label: "No", Value: "no"},
	}

	selected := 1
	if defaultYes {
		selected = 0
	}

	// Put terminal in raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return false, err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	fmt.Print(hideCursor)
	defer fmt.Print(showCursor)

	fmt.Printf("\r\n  %s%s%s\r\n\r\n", colorYellow, prompt, colorReset)
	renderInlineChoice(items, selected)

	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return false, err
		}

		switch {
		case n == 1 && buf[0] == 13: // Enter
			fmt.Printf("\r\n")
			return selected == 0, nil
		case n == 1 && buf[0] == 3: // Ctrl+C
			fmt.Printf("\r\n")
			return false, fmt.Errorf("cancelled")
		case n == 3 && buf[0] == 27 && buf[1] == 91 && (buf[2] == 68 || buf[2] == 65): // Left/Up
			selected = 0
		case n == 3 && buf[0] == 27 && buf[1] == 91 && (buf[2] == 67 || buf[2] == 66): // Right/Down
			selected = 1
		case n == 1 && (buf[0] == 'y' || buf[0] == 'Y'):
			fmt.Printf("\r\n")
			return true, nil
		case n == 1 && (buf[0] == 'n' || buf[0] == 'N'):
			fmt.Printf("\r\n")
			return false, nil
		}

		fmt.Printf(moveToCol)
		fmt.Printf(moveUp, 1)
		renderInlineChoice(items, selected)
	}
}

func renderInlineChoice(items []MenuItem, selected int) {
	fmt.Print(clearLine)
	fmt.Print("\r    ")
	for i, item := range items {
		if i == selected {
			fmt.Printf("%s❯ %s%s  ", colorCyan, item.Label, colorReset)
		} else {
			fmt.Printf("  %s  ", item.Label)
		}
	}
	fmt.Print("\r\n")
}

// TextInput shows a prompt and reads user text input (in cooked mode).
// Temporarily restores normal terminal mode for text entry.
func TextInput(prompt string, defaultValue string) (string, error) {
	if defaultValue != "" {
		fmt.Printf("\r\n  %s %s(%s)%s: ", prompt, colorDim, defaultValue, colorReset)
	} else {
		fmt.Printf("\r\n  %s: ", prompt)
	}

	// Read line in normal mode
	var input strings.Builder
	buf := make([]byte, 1)
	for {
		_, err := os.Stdin.Read(buf)
		if err != nil {
			return "", err
		}
		if buf[0] == 13 || buf[0] == 10 { // Enter
			fmt.Print("\r\n")
			break
		}
		if buf[0] == 127 || buf[0] == 8 { // Backspace
			s := input.String()
			if len(s) > 0 {
				input.Reset()
				input.WriteString(s[:len(s)-1])
				fmt.Print("\b \b") // Erase character on screen
			}
			continue
		}
		if buf[0] == 3 { // Ctrl+C
			return "", fmt.Errorf("cancelled")
		}
		if buf[0] >= 32 && buf[0] < 127 { // Printable ASCII
			input.WriteByte(buf[0])
			fmt.Printf("%c", buf[0])
		}
	}

	result := input.String()
	if result == "" {
		return defaultValue, nil
	}
	return result, nil
}

// Header prints a styled section header.
func Header(text string) {
	width := 60
	fmt.Printf("\r\n  %s%s%s\r\n", colorBold, text, colorReset)
	fmt.Printf("  %s%s%s\r\n\r\n", colorDim, strings.Repeat("─", width), colorReset)
}

// Success prints a green success message.
func Success(text string) {
	fmt.Printf("  %s✓ %s%s\r\n", colorGreen, text, colorReset)
}

// Warning prints a yellow warning message.
func Warning(text string) {
	fmt.Printf("  %s⚠️  %s%s\r\n", colorYellow, text, colorReset)
}

// Info prints an informational message.
func Info(text string) {
	fmt.Printf("  %s%s%s\r\n", colorDim, text, colorReset)
}

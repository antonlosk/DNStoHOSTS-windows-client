package main

import (
	"bufio"
	"os"
	"strings"
)

// Config holds the application settings
type Config struct {
	Server string
	Port   string
	IPv4   bool
	IPv6   bool
	Theme  string // Stores the current theme (e.g., "black" or "white")
}

// loadSettings reads the configuration from settings.txt
func loadSettings() Config {
	c := Config{Server: "dns.google", Port: "443", IPv4: true, IPv6: false, Theme: "black"}
	f, err := os.Open("settings.txt")
	if err != nil {
		return c
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		p := strings.SplitN(s.Text(), "=", 2)
		if len(p) < 2 {
			continue
		}
		k, v := strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
		switch k {
		case "server":
			c.Server = v
		case "port":
			c.Port = v
		case "ipv4":
			c.IPv4 = (v == "true")
		case "ipv6":
			c.IPv6 = (v == "true")
		case "theme":
			c.Theme = v
		}
	}
	return c
}

// saveThemeSetting updates only the theme value in settings.txt
func saveThemeSetting(isDark bool) {
	themeVal := "white"
	if isDark {
		themeVal = "black"
	}

	lines, err := readLines("settings.txt")
	if err != nil {
		return // Skip if file is missing
	}

	updated := false
	for i, line := range lines {
		// Look for the theme setting line
		if strings.HasPrefix(strings.TrimSpace(line), "theme=") {
			lines[i] = "theme=" + themeVal
			updated = true
			break
		}
	}

	// Add theme setting if it wasn't in the file
	if !updated {
		lines = append(lines, "theme="+themeVal)
	}

	writeLines("settings.txt", lines)
}
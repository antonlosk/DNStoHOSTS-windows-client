package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ensureFilesExist creates default files if they are missing
func ensureFilesExist() {
	if _, err := os.Stat("input.txt"); os.IsNotExist(err) {
		os.WriteFile("input.txt", []byte("# List domains here\ngoogle.com\n"), 0644)
	}
	if _, err := os.Stat("settings.txt"); os.IsNotExist(err) {
		// Default theme is black
		os.WriteFile("settings.txt", []byte("server=dns.google\nport=443\nipv4=true\nipv6=false\ntheme=black\n"), 0644)
	}
}

// readLines reads all lines from a file
func readLines(p string) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var l []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		l = append(l, s.Text())
	}
	return l, s.Err()
}

// writeLines writes a slice of strings to a file
func writeLines(p string, l []string) {
	f, _ := os.Create(p)
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, line := range l {
		w.WriteString(line + "\n")
	}
	w.Flush()
}

// openFile opens a file using the default OS application
func openFile(fn string) {
	path, _ := filepath.Abs(fn)
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	case "darwin":
		exec.Command("open", path).Start()
	default:
		exec.Command("xdg-open", path).Start()
	}
}
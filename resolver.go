package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// startResolving handles the main business logic of reading, fetching, and saving DNS mappings
func (ui *UI) startResolving(ctx context.Context) {
	ui.addLog("Process started...")
	cfg := loadSettings()
	lines, err := readLines("input.txt")
	if err != nil {
		ui.addLog("Error: " + err.Error())
		ui.finish(StateIdle)
		return
	}

	var tasks []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			tasks = append(tasks, l)
		}
	}

	ui.StateMutex.Lock()
	ui.TotalLines, ui.CurrentLine = len(tasks), 0
	ui.StateMutex.Unlock()

	httpClient := &http.Client{Timeout: 10 * time.Second}
	dURL := fmt.Sprintf("https://%s:%s/dns-query", cfg.Server, cfg.Port)
	if cfg.Port == "" {
		dURL = fmt.Sprintf("https://%s/dns-query", cfg.Server)
	}

	var output []string
	for _, line := range tasks {
		select {
		case <-ctx.Done():
			ui.addLog("Cancelled.")
			ui.finish(StateCancelled)
			return
		default:
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			output = append(output, trimmed)
			ui.addLog(trimmed)
			ui.incrementProgress()
			continue
		}

		ui.addLog("Resolving: " + trimmed)
		var foundIps []string

		resolve := func(qtype uint16, name string) {
			ips, err := resolveBinaryDoH(ctx, httpClient, dURL, trimmed, qtype)
			if err != nil {
				ui.addLog(fmt.Sprintf("   [%s] Error: %v", name, err))
			} else {
				foundIps = append(foundIps, ips...)
				for _, ip := range ips {
					ui.addLog(fmt.Sprintf("   [%s] Found: %s", name, ip))
				}
			}
		}

		if cfg.IPv4 {
			resolve(dns.TypeA, "A")
		}
		if cfg.IPv6 {
			resolve(dns.TypeAAAA, "AAAA")
		}

		if len(foundIps) == 0 {
			output = append(output, "# Not found: "+trimmed)
		} else {
			for _, ip := range foundIps {
				output = append(output, fmt.Sprintf("%s %s", ip, trimmed))
			}
		}
		ui.incrementProgress()
	}

	writeLines("output.txt", output)
	ui.addLog("Process finished. Results saved.")
	ui.finish(StateDone)
}

// resolveBinaryDoH makes the actual DNS-over-HTTPS request using the miekg/dns library
func resolveBinaryDoH(ctx context.Context, client *http.Client, url, domain string, qtype uint16) ([]string, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true
	buf, _ := m.Pack()

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/dns-message")
		req.Header.Set("Accept", "application/dns-message")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				time.Sleep(time.Second)
				continue
			}
			return nil, lastErr
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		respMsg := new(dns.Msg)
		if err := respMsg.Unpack(body); err != nil {
			return nil, err
		}

		var ips []string
		for _, a := range respMsg.Answer {
			if t, ok := a.(*dns.A); ok && qtype == dns.TypeA {
				ips = append(ips, t.A.String())
			}
			if t, ok := a.(*dns.AAAA); ok && qtype == dns.TypeAAAA {
				ips = append(ips, t.AAAA.String())
			}
		}
		return ips, nil
	}
	return nil, lastErr
}
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Media;
using DnsClient; // Библиотека для работы с DNS

namespace DNStoHOSTS
{
    public partial class MainWindow : Window
    {
        private const string InputFile = "input.txt";
        private const string SettingsFile = "settings.txt";
        private const string OutputFile = "output.txt";

        private bool _isDarkTheme = true;
        private CancellationTokenSource _cts;
        private static readonly HttpClient _httpClient = new HttpClient();

        public MainWindow()
        {
            InitializeComponent();
            CheckAndCreateDefaultFiles();
        }

        private void CheckAndCreateDefaultFiles()
        {
            try
            {
                if (!File.Exists(InputFile)) File.WriteAllText(InputFile, "# Google\r\ngoogle.com\r\n");
                if (!File.Exists(SettingsFile)) File.WriteAllText(SettingsFile, "server=dns.google\r\nport=443\r\nipv4=true\r\nipv6=false\r\n");
            }
            catch { }
        }

        private void Log(string message)
        {
            Dispatcher.Invoke(() =>
            {
                LogTextBox.AppendText($"[{DateTime.Now:HH:mm:ss}] {message}\r\n");
                LogTextBox.ScrollToEnd();
            });
        }

        private void BtnTheme_Click(object sender, RoutedEventArgs e)
        {
            _isDarkTheme = !_isDarkTheme;
            if (_isDarkTheme) SetColors("#1E1E1E", "#252526", "#D4D4D4", "#333333");
            else SetColors("#F3F3F3", "#FFFFFF", "#000000", "#E1E1E1");
        }

        private void SetColors(string win, string log, string text, string btn)
        {
            Resources["WindowBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(win));
            Resources["LogBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(log));
            Resources["TextColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(text));
            Resources["ButtonBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(btn));
        }

        private void BtnFile_Click(object sender, RoutedEventArgs e)
        {
            if (sender is System.Windows.Controls.Button btn && btn.Tag is string fn && File.Exists(fn))
                Process.Start(new ProcessStartInfo(fn) { UseShellExecute = true });
        }

        private void BtnClear_Click(object sender, RoutedEventArgs e) => LogTextBox.Clear();

        private void BtnStop_Click(object sender, RoutedEventArgs e) => _cts?.Cancel();

        private async void BtnStart_Click(object sender, RoutedEventArgs e)
        {
            if (_cts != null) return;
            _cts = new CancellationTokenSource();
            MainProgress.Foreground = (SolidColorBrush)FindResource("ProgressBlue");

            try 
            { 
                await Task.Run(() => ProcessDomains(_cts.Token)); 
                MainProgress.Foreground = (SolidColorBrush)FindResource("ProgressGreen"); 
            }
            catch (OperationCanceledException) { Log("Stopped."); }
            catch (Exception ex) { Log("Error: " + ex.Message); }
            finally { _cts.Dispose(); _cts = null; }
        }

        private async Task ProcessDomains(CancellationToken token)
        {
            Log("Starting...");
            var settings = ReadSettings();
            if (!File.Exists(InputFile)) return;

            var lines = File.ReadAllLines(InputFile);
            var output = new List<string>();
            var targets = lines.Where(l => !l.TrimStart().StartsWith("#") && !string.IsNullOrWhiteSpace(l)).ToList();

            Dispatcher.Invoke(() => { MainProgress.Maximum = targets.Count; MainProgress.Value = 0; });

            int count = 0;
            foreach (var line in lines)
            {
                token.ThrowIfCancellationRequested();
                if (string.IsNullOrWhiteSpace(line) || line.Trim().StartsWith("#")) { output.Add(line); continue; }

                string domain = line.Trim();
                Log("Resolving: " + domain);
                
                var ips = new List<string>();
                var errors = new List<string>();
                
                // Используем библиотеку для каждого типа запроса
                if (settings.ipv4) ips.AddRange(await ResolveWithLib(domain, QueryType.A, settings, token, errors));
                if (settings.ipv6) ips.AddRange(await ResolveWithLib(domain, QueryType.AAAA, settings, token, errors));

                if (!ips.Any())
                {
                    foreach(var err in errors.Distinct()) Log($"  [!] {err}");
                    output.Add("# No records: " + domain);
                }
                else 
                {
                    foreach (var ip in ips.Distinct()) 
                    { 
                        Log("  -> " + ip); 
                        output.Add($"{ip} {domain}"); 
                    }
                }

                count++;
                Dispatcher.Invoke(() => MainProgress.Value = count);
            }
            File.WriteAllLines(OutputFile, output);
            Log("Done. Saved to " + OutputFile);
        }

        private async Task<List<string>> ResolveWithLib(string domain, QueryType type, dynamic s, CancellationToken t, List<string> errs)
        {
            var results = new List<string>();
            try
            {
                // Используем DnsClient для создания бинарного запроса
                var msgHandler = new DnsMessageHandler();
                var query = new DnsQuestion(domain, type);
                var header = new DnsRequestHeader(Guid.NewGuid().GetHashCode(), true, DnsOpCode.Query);
                var requestData = msgHandler.GetRequestData(new DnsRequestMessage(header, query));

                // Кодируем в Base64Url
                string base64 = Convert.ToBase64String(requestData.ToArray()).Replace('+', '-').Replace('/', '_').TrimEnd('=');
                string url = $"https://{s.server}:{s.port}/dns-query?dns={base64}";

                using (var req = new HttpRequestMessage(HttpMethod.Get, url))
                {
                    req.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/dns-message"));
                    req.Headers.UserAgent.ParseAdd("DNStoHOSTS/1.2");

                    var resp = await _httpClient.SendAsync(req, t);
                    if (resp.IsSuccessStatusCode)
                    {
                        var data = await resp.Content.ReadAsByteArrayAsync();
                        var responseMessage = msgHandler.GetResponseMessage(data);
                        foreach (var answer in responseMessage.Answers)
                        {
                            if (answer is DnsClient.Protocol.AddressRecord addr)
                                results.Add(addr.Address.ToString());
                        }
                    }
                    else errs.Add($"HTTP {(int)resp.StatusCode}");
                }
            }
            catch (Exception ex) { errs.Add(ex.Message); }
            return results;
        }

        private dynamic ReadSettings()
        {
            string s = "dns.google"; int p = 443; bool v4 = true, v6 = false;
            if (File.Exists(SettingsFile))
                foreach (var l in File.ReadAllLines(SettingsFile)) {
                    var x = l.Split('='); if (x.Length != 2) continue;
                    var k = x[0].Trim().ToLower(); var v = x[1].Trim().ToLower();
                    if (k == "server") s = v; else if (k == "port") int.TryParse(v, out p);
                    else if (k == "ipv4") v4 = v == "true"; else if (k == "ipv6") v6 = v == "true";
                }
            return new { server = s, port = p, ipv4 = v4, ipv6 = v6 };
        }
    }
}
